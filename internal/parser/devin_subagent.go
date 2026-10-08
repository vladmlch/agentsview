package parser

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"slices"
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

// partitionDevinSubagentMessageNodes splits a session's message-node forest
// into subagent trees and the node ids they claim. mainChainRootID is the
// resolved main chain's root (-1 when unresolved); it and the earliest
// parentless node -- the session's own head in any Devin database -- are never
// treated as subagent or summarizer roots, so a root prompt merely quoting
// the markers cannot empty the session.
func partitionDevinSubagentMessageNodes(
	ctx context.Context,
	rows []devinMessageNodeRow,
	mainChainRootID int64,
) (devinSubagentNodePartition, error) {
	partition := devinSubagentNodePartition{
		ExcludedNodeIDs: make(map[int64]struct{}),
	}
	if len(rows) == 0 {
		return partition, nil
	}

	indexByNodeID := make(map[int64]int, len(rows))
	childrenByParent := make(map[int64][]int64, len(rows))
	treeIndexByPromptMessageID := make(map[string]int)
	earliestRootID := int64(-1)
	for i, row := range rows {
		indexByNodeID[row.NodeID] = i
		if row.ParentNodeID.Valid {
			childrenByParent[row.ParentNodeID.Int64] = append(
				childrenByParent[row.ParentNodeID.Int64], row.NodeID,
			)
		} else if earliestRootID < 0 {
			earliestRootID = row.NodeID
		}
	}

	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return devinSubagentNodePartition{}, err
		}
		if row.ParentNodeID.Valid {
			continue
		}
		if row.NodeID == mainChainRootID || row.NodeID == earliestRootID {
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
			tree := devinSubagentNodeTree{
				RootNodeID: rootNodeID,
				Rows:       treeRows,
			}
			promptMessageID := devinSubagentTreePromptMessageID(treeRows)
			if promptMessageID == "" {
				partition.Trees = append(partition.Trees, tree)
				continue
			}
			if existingIndex, exists := treeIndexByPromptMessageID[promptMessageID]; exists {
				existing := partition.Trees[existingIndex]
				if len(tree.Rows) > len(existing.Rows) ||
					(len(tree.Rows) == len(existing.Rows) && tree.RootNodeID > existing.RootNodeID) {
					partition.Trees[existingIndex] = tree
				}
				continue
			}
			treeIndexByPromptMessageID[promptMessageID] = len(partition.Trees)
			partition.Trees = append(partition.Trees, tree)
		}
	}
	sort.Slice(partition.Trees, func(i, j int) bool {
		return partition.Trees[i].RootNodeID < partition.Trees[j].RootNodeID
	})
	return partition, nil
}

// devinSubagentTreePromptMessageID identifies copies of the same logical
// subagent transcript. Devin can persist multiple disconnected roots for one
// task; those copies retain the message_id of their first user prompt while
// receiving different root node IDs.
func devinSubagentTreePromptMessageID(rows []devinMessageNodeRow) string {
	var (
		messageID   string
		firstNodeID int64
	)
	for _, row := range rows {
		if gjson.Get(row.ChatMessage, "role").Str != "user" {
			continue
		}
		candidate := strings.TrimSpace(devinNodeMessageID(row.ChatMessage))
		if candidate == "" {
			continue
		}
		if messageID == "" || row.NodeID < firstNodeID {
			messageID = candidate
			firstNodeID = row.NodeID
		}
	}
	return messageID
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
	return devinRootMarkerKind(content)
}

// devinRootMarkerKind anchors the subagent/summarizer markers at the start of
// the node's text. Devin writes these prompts as the first line of the tree
// root; an unanchored match would also fire on the main session's own system
// prompt whenever it quotes the marker (for example inside an embedded
// subagent profile list).
func devinRootMarkerKind(text string) devinMessageNodeRoot {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "'\"")
	text = strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(text, devinSubagentRootMarker):
		return devinMessageNodeRootSubagent
	case strings.HasPrefix(text, devinSummarizerRootMarker):
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
			if ctx.Err() != nil {
				return nil, err
			}
			// A malformed node inside one tree must not take the whole
			// session down: drop that tree and keep the rest.
			log.Printf(
				"devin: dropping malformed subagent message tree (root node %d): %v",
				tree.RootNodeID, err,
			)
			continue
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
	for _, row := range devinSubagentTreeRows(tree.Rows) {
		if err := ctx.Err(); err != nil {
			return devinParsedSubagent{}, false, err
		}
		message, ok, err := parseDevinDBMessageNode(
			rawSessionID, row, len(messages), model,
		)
		if err != nil {
			return devinParsedSubagent{}, false, err
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

// devinSubagentTreeRows resolves a subagent tree down to its surviving
// spine, the same way era recovery walks a recovered thread. Devin persists
// pending and committed states of one logical message as sibling nodes
// sharing a message_id; the higher node_id is the final write and wins.
// Siblings with distinct message_ids are alternate timelines (retries,
// edited turns); the deepest subtree is the line the subagent actually
// continued on. Nodes without a message_id always emit. The result keeps
// conversation order: root first, then the resolved child, and so on.
func devinSubagentTreeRows(treeRows []devinMessageNodeRow) []devinMessageNodeRow {
	inTree := make(map[int64]bool, len(treeRows))
	for _, row := range treeRows {
		inTree[row.NodeID] = true
	}
	children := make(map[int64][]devinMessageNodeRow, len(treeRows))
	var root devinMessageNodeRow
	found := false
	for _, row := range treeRows {
		if row.ParentNodeID.Valid && inTree[row.ParentNodeID.Int64] {
			children[row.ParentNodeID.Int64] = append(
				children[row.ParentNodeID.Int64], row,
			)
			continue
		}
		root = row
		found = true
	}
	if !found {
		return nil
	}

	depthMemo := make(map[int64]int, len(treeRows))
	visiting := make(map[int64]bool)
	var depth func(id int64) int
	depth = func(id int64) int {
		if d, ok := depthMemo[id]; ok {
			return d
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		d := 1
		for _, child := range children[id] {
			if cd := depth(child.NodeID) + 1; cd > d {
				d = cd
			}
		}
		delete(visiting, id)
		depthMemo[id] = d
		return d
	}

	seenMessageIDs := make(map[string]bool, len(treeRows))
	var out []devinMessageNodeRow
	cur := root
	for walked := 0; walked <= len(treeRows); walked++ {
		mid := devinNodeMessageID(cur.ChatMessage)
		if mid == "" || !seenMessageIDs[mid] {
			out = append(out, cur)
			if mid != "" {
				seenMessageIDs[mid] = true
			}
		}
		kids := children[cur.NodeID]
		if len(kids) == 0 {
			break
		}
		distinct := make(map[string]bool, len(kids))
		for _, kid := range kids {
			distinct[devinNodeMessageID(kid.ChatMessage)] = true
		}
		if len(distinct) == 1 {
			// pending/committed twins of one logical message: the committed
			// copy (higher node_id) wins.
			cur = slices.MaxFunc(kids, func(a, b devinMessageNodeRow) int {
				return cmp.Compare(a.NodeID, b.NodeID)
			})
			continue
		}
		// Alternate timelines: keep the line the run continued furthest on.
		cur = slices.MaxFunc(kids, func(a, b devinMessageNodeRow) int {
			return cmp.Compare(depth(a.NodeID), depth(b.NodeID))
		})
	}
	return out
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

func devinSubagentExists(
	ctx context.Context,
	dbPath, rawSessionID string,
	rootNodeID int64,
) (bool, error) {
	rows, err := listDevinMessageNodes(ctx, dbPath, rawSessionID)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	// No session metadata is available here, so the resolved main-chain root
	// is unknown (-1); the earliest parentless node is still never a subagent.
	partition, err := partitionDevinSubagentMessageNodes(ctx, rows, -1)
	if err != nil {
		return false, err
	}
	for _, tree := range partition.Trees {
		if tree.RootNodeID == rootNodeID {
			return true, nil
		}
	}
	return false, nil
}
