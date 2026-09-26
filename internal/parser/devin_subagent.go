package parser

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	devinSubagentRootMarker   = "You are a subagent of Devin"
	devinSummarizerRootMarker = "You are a Summarizer"
)

type devinSubagentNodeTree struct {
	RootNodeID int64
	Rows       []devinMessageNodeRow
}

type devinSubagentNodePartition struct {
	Trees           []devinSubagentNodeTree
	ExcludedNodeIDs map[int64]struct{}
}

type devinParsedSubagent struct {
	Session  ParsedSession
	Messages []ParsedMessage
}

type devinSubagentParseError struct {
	RootNodeID int64
	Err        error
}

func (e *devinSubagentParseError) Error() string {
	return fmt.Sprintf(
		"parsing Devin subagent root node %d: %v", e.RootNodeID, e.Err,
	)
}

func (e *devinSubagentParseError) Unwrap() error {
	return e.Err
}

func partitionDevinSubagentMessageNodes(
	ctx context.Context,
	rows []devinMessageNodeRow,
) (devinSubagentNodePartition, error) {
	partition := devinSubagentNodePartition{
		ExcludedNodeIDs: make(map[int64]struct{}),
	}
	if len(rows) == 0 {
		return partition, nil
	}

	indexByNodeID := make(map[int64]int, len(rows))
	childrenByParent := make(map[int64][]int64, len(rows))
	for i, row := range rows {
		indexByNodeID[row.NodeID] = i
		if row.ParentNodeID.Valid {
			childrenByParent[row.ParentNodeID.Int64] = append(
				childrenByParent[row.ParentNodeID.Int64], row.NodeID,
			)
		}
	}

	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return devinSubagentNodePartition{}, err
		}
		if row.ParentNodeID.Valid {
			continue
		}
		kind := devinMessageNodeRootKind(ctx, row.ChatMessage)
		if kind == devinMessageNodeRootNone {
			continue
		}

		rootNodeID := row.NodeID
		visited := map[int64]struct{}{rootNodeID: {}}
		stack := []int64{rootNodeID}
		indices := []int{i}
		for len(stack) > 0 {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, childNodeID := range childrenByParent[current] {
				if _, seen := visited[childNodeID]; seen {
					continue
				}
				childIndex, exists := indexByNodeID[childNodeID]
				if !exists {
					continue
				}
				visited[childNodeID] = struct{}{}
				indices = append(indices, childIndex)
				stack = append(stack, childNodeID)
			}
		}
		sort.Ints(indices)
		treeRows := make([]devinMessageNodeRow, 0, len(indices))
		for _, rowIndex := range indices {
			treeRows = append(treeRows, rows[rowIndex])
			partition.ExcludedNodeIDs[rows[rowIndex].NodeID] = struct{}{}
		}
		if kind == devinMessageNodeRootSubagent && len(treeRows) > 1 {
			partition.Trees = append(partition.Trees, devinSubagentNodeTree{
				RootNodeID: rootNodeID,
				Rows:       treeRows,
			})
		}
	}
	return partition, nil
}

type devinMessageNodeRoot int

const (
	devinMessageNodeRootNone devinMessageNodeRoot = iota
	devinMessageNodeRootSubagent
	devinMessageNodeRootSummarizer
)

func devinMessageNodeRootKind(
	ctx context.Context,
	chatMessage string,
) devinMessageNodeRoot {
	if !gjson.Valid(chatMessage) {
		return devinMessageNodeRootNone
	}
	message := gjson.Parse(chatMessage)
	if message.Get("role").Str != "system" {
		return devinMessageNodeRootNone
	}
	content, _, _, _, _, _ := ExtractTextContent(ctx, message.Get("content"))
	switch {
	case strings.Contains(content, devinSubagentRootMarker):
		return devinMessageNodeRootSubagent
	case strings.Contains(content, devinSummarizerRootMarker):
		return devinMessageNodeRootSummarizer
	default:
		return devinMessageNodeRootNone
	}
}

func parseDevinSubagentNodeTrees(
	ctx context.Context,
	rawSessionID, machine string,
	meta *DevinSessionMeta,
	parentFile FileInfo,
	trees []devinSubagentNodeTree,
) ([]devinParsedSubagent, error) {
	parsed := make([]devinParsedSubagent, 0, len(trees))
	for _, tree := range trees {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		child, ok, err := parseDevinSubagentNodeTree(
			ctx, rawSessionID, machine, meta, parentFile, tree,
		)
		if err != nil {
			return nil, err
		}
		if ok {
			parsed = append(parsed, child)
		}
	}
	return parsed, nil
}

func parseDevinSubagentNodeTree(
	ctx context.Context,
	rawSessionID, machine string,
	meta *DevinSessionMeta,
	parentFile FileInfo,
	tree devinSubagentNodeTree,
) (devinParsedSubagent, bool, error) {
	model := metaValue(meta, func(m *DevinSessionMeta) string { return m.Model })
	messages := make([]ParsedMessage, 0, len(tree.Rows))
	var (
		firstMessage string
		firstStepAt  = time.Time{}
		lastStepAt   = time.Time{}
		userMsgCount int
	)
	for _, row := range tree.Rows {
		if err := ctx.Err(); err != nil {
			return devinParsedSubagent{}, false, err
		}
		message, ok, err := parseDevinDBMessageNode(
			rawSessionID, row, len(messages), model,
		)
		if err != nil {
			return devinParsedSubagent{}, false, &devinSubagentParseError{
				RootNodeID: tree.RootNodeID,
				Err:        err,
			}
		}
		if !ok {
			continue
		}
		messages = append(messages, message)
		if firstStepAt.IsZero() && !message.Timestamp.IsZero() {
			firstStepAt = message.Timestamp
		}
		if message.Timestamp.After(lastStepAt) {
			lastStepAt = message.Timestamp
		}
		if message.Role == RoleUser && !message.IsSystem &&
			strings.TrimSpace(message.Content) != "" {
			userMsgCount++
			if firstMessage == "" {
				firstMessage = truncate(
					strings.ReplaceAll(message.Content, "\n", " "), 300,
				)
			}
		}
	}
	if len(messages) == 0 {
		return devinParsedSubagent{}, false, nil
	}

	sourceSessionID := devinSubagentRawSessionID(rawSessionID, tree.RootNodeID)
	sessionID := string(AgentDevin) + ":" + sourceSessionID
	startedAt := firstNonZeroTime(
		firstStepAt,
		devinUnixSec(tree.Rows[0].CreatedAt),
		metaTime(meta, func(m *DevinSessionMeta) time.Time { return m.CreatedAt }),
	)
	endedAt := firstNonZeroTime(lastStepAt, startedAt)
	sessionName := firstMessage
	if sessionName == "" {
		sessionName = fmt.Sprintf("Subagent %d", tree.RootNodeID)
	}
	fileInfo := parentFile
	fileInfo.Path = devinSubagentVirtualSourcePath(parentFile.Path, tree.RootNodeID)
	devinApplyFileInfoTimes(&fileInfo, meta, endedAt)

	session := ParsedSession{
		ID:               sessionID,
		Project:          ExtractProjectFromCwd(metaValue(meta, func(m *DevinSessionMeta) string { return m.CWD })),
		Machine:          machine,
		Agent:            AgentDevin,
		ParentSessionID:  string(AgentDevin) + ":" + rawSessionID,
		RelationshipType: RelSubagent,
		Cwd:              metaValue(meta, func(m *DevinSessionMeta) string { return m.CWD }),
		SourceSessionID:  sourceSessionID,
		FirstMessage:     firstMessage,
		SessionName:      sessionName,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		MessageCount:     len(messages),
		UserMessageCount: userMsgCount,
		File:             fileInfo,
	}
	accumulateMessageTokenUsage(&session, messages)
	return devinParsedSubagent{Session: session, Messages: messages}, true, nil
}

func devinSubagentRawSessionID(parentSessionID string, rootNodeID int64) string {
	return fmt.Sprintf("%s:agent-%d", parentSessionID, rootNodeID)
}

func parseDevinSubagentRawSessionID(rawSessionID string) (
	parentSessionID string,
	rootNodeID int64,
	ok bool,
) {
	separator := strings.LastIndex(rawSessionID, ":agent-")
	if separator <= 0 {
		return "", 0, false
	}
	rootNodeID, err := strconv.ParseInt(rawSessionID[separator+len(":agent-"):], 10, 64)
	if err != nil || rootNodeID < 0 {
		return "", 0, false
	}
	return rawSessionID[:separator], rootNodeID, true
}

func devinSubagentVirtualSourcePath(parentPath string, rootNodeID int64) string {
	return VirtualSourcePath(parentPath, fmt.Sprintf("agent-%d", rootNodeID))
}

func parseDevinSubagentVirtualSourcePath(
	path string,
) (dbPath, parentSessionID string, rootNodeID int64, ok bool) {
	parentPath, memberID, ok := ParseVirtualSourcePath(path)
	if !ok {
		return "", "", 0, false
	}
	dbPath, parentSessionID, ok = ParseVirtualSourcePathForBase(
		parentPath, devinDBFilename,
	)
	if !ok || parentSessionID == "" {
		return "", "", 0, false
	}
	memberID = strings.TrimPrefix(memberID, "agent-")
	rootNodeID, err := strconv.ParseInt(memberID, 10, 64)
	if err != nil || rootNodeID < 0 {
		return "", "", 0, false
	}
	return dbPath, parentSessionID, rootNodeID, true
}

func devinMessageNodesWithout(
	rows []devinMessageNodeRow,
	excludedNodeIDs map[int64]struct{},
) []devinMessageNodeRow {
	if len(excludedNodeIDs) == 0 {
		return rows
	}
	filtered := make([]devinMessageNodeRow, 0, len(rows))
	for _, row := range rows {
		if _, excluded := excludedNodeIDs[row.NodeID]; excluded {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}
