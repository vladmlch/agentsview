package parser

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// findJunieStream matches an event target identifier or stepId to an existing
// subagent stream in streams.
func findJunieStream(
	streams map[string]*junieAgentStream,
	targetAgent, stepID string,
) *junieAgentStream {
	if targetAgent == "main" {
		return streams["main"]
	}
	cleanStep := strings.TrimSuffix(strings.TrimSuffix(stepID, "-request"), "-spawned")
	// 1. Exact match on targetAgent as stream ID
	if targetAgent != "" {
		if st := streams[targetAgent]; st != nil {
			return st
		}
	}
	// 2. Exact match on stepID as stream stepID
	if cleanStep != "" {
		for _, st := range streams {
			if st.id != "main" && (st.stepID == cleanStep || st.stepID == stepID) {
				return st
			}
		}
	}
	// 3. Match by name or taskName
	if targetAgent != "" {
		for _, st := range streams {
			if st.id != "main" && (st.name == targetAgent || st.taskName == targetAgent) {
				return st
			}
		}
		// Match targetAgent against stream stepID
		for _, st := range streams {
			if st.id != "main" && (st.stepID == targetAgent || strings.TrimSuffix(targetAgent, "-request") == st.stepID) {
				return st
			}
		}
	}
	// 4. Check if stepID is in any stream's blocks
	if stepID != "" {
		for _, st := range streams {
			if st.id != "main" {
				if _, ok := st.blocks[stepID]; ok {
					return st
				}
			}
		}
	}
	return nil
}

func junieFinishSubagent(
	ctx context.Context,
	fullID, parentFullID, path string, info os.FileInfo,
	stream *junieAgentStream,
	summary junieSessionSummary, summaryPresent bool,
	machine, hash, stateCwd, rawSubID string,
) (*ParsedSession, []ParsedMessage, bool) {
	// A subagent session must have actual activity: either assignment prompt or blocks.
	if stream.request == "" && len(stream.blocks) == 0 {
		return nil, nil, false
	}

	var extra []ParsedMessage
	if stream.request != "" {
		extra = append(extra, ParsedMessage{
			Role:          RoleUser,
			Content:       stream.request,
			ContentLength: len(stream.request),
			Timestamp:     stream.requestTS,
		})
	}
	msgs := junieAssembleMessages(stream, extra)
	if len(msgs) == 0 {
		return nil, nil, false
	}
	cwd := strings.TrimSpace(stream.cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(summary.projectDir)
	}
	if cwd == "" {
		cwd = stateCwd
	}
	if filepath.IsAbs(cwd) {
		cwd = filepath.Clean(cwd)
	} else if !strings.HasPrefix(cwd, "/") && !looksLikeWindowsPath(cwd) {
		cwd = ""
	}

	project := ExtractProjectFromCwdWithBranchContext(
		WithoutFilesystemProjectDiscovery(ctx), cwd, "",
	)
	if project == "" {
		project = "junie"
	}

	startedAt := stream.firstTS
	if startedAt.IsZero() {
		startedAt = stream.requestTS
	}
	if startedAt.IsZero() {
		startedAt = summary.createdAt
	}
	if startedAt.IsZero() {
		startedAt = junieTimestampFromID(fullID)
	}
	if startedAt.IsZero() && info != nil {
		startedAt = info.ModTime().UTC()
	}

	endedAt := stream.lastTS
	if !summary.updatedAt.IsZero() && summary.updatedAt.After(endedAt) {
		endedAt = summary.updatedAt
	}
	if endedAt.IsZero() || endedAt.Before(startedAt) {
		if info != nil && info.ModTime().UTC().After(startedAt) {
			endedAt = info.ModTime().UTC()
		} else {
			endedAt = startedAt
		}
	}

	for i := range msgs {
		if msgs[i].Timestamp.IsZero() {
			msgs[i].Timestamp = startedAt
		}
	}

	subName := firstNonEmptyJSONLString(stream.name, stream.taskName)
	var firstMessage string
	if stream.request != "" {
		firstMessage = truncate(strings.ReplaceAll(stream.request, "\n", " "), 300)
	} else {
		for _, m := range msgs {
			if m.Role == RoleUser && !m.IsSystem {
				firstMessage = truncate(strings.ReplaceAll(m.Content, "\n", " "), 300)
				break
			}
		}
	}
	if firstMessage == "" && strings.TrimSpace(subName) != "" {
		firstMessage = subName
	}

	fileInfo := FileInfo{
		Path: VirtualSourcePath(path, stream.id),
		Hash: hash,
	}
	if info != nil {
		fileInfo.Size = info.Size()
		fileInfo.Mtime = info.ModTime().UnixNano()
	}
	sess := &ParsedSession{
		ID:                 fullID,
		SourceSessionID:    rawSubID,
		Project:            project,
		Agent:              AgentJunie,
		Machine:            machine,
		ParentSessionID:    parentFullID,
		RelationshipType:   RelSubagent,
		SessionName:        subName,
		SessionNamePresent: subName != "",
		FirstMessage:       firstMessage,
		Cwd:                cwd,
		StartedAt:          startedAt,
		EndedAt:            endedAt,
		MessageCount:       len(msgs),
		File:               fileInfo,
	}
	for i := range msgs {
		if msgs[i].Role == RoleUser && !msgs[i].IsSystem {
			sess.UserMessageCount++
		}
	}
	hasFailureOrCancel := stream.cancelled || stream.failed ||
		stream.state == "CANCELLED" || stream.state == "CANCELED" || stream.state == "FAILED"
	if !hasFailureOrCancel {
		for _, n := range stream.notes {
			if n.SourceSubtype == "AgentFailureEvent" ||
				n.SourceSubtype == "AgentTaskFailedEvent" ||
				n.SourceSubtype == "CancelAgentEvent" {
				hasFailureOrCancel = true
				break
			}
		}
	}
	if !hasFailureOrCancel {
		for _, b := range stream.blocks {
			if b.cancelled ||
				b.status == "FAILED" ||
				b.status == "CANCELLED" ||
				b.status == "CANCELED" ||
				b.status == "ERROR" {
				hasFailureOrCancel = true
				break
			}
		}
	}
	if hasFailureOrCancel {
		sess.TerminationStatus = TerminationTruncated
	} else {
		sess.TerminationStatus = Classify(msgs, "", false)
	}
	sess.UsageEvents = junieRewriteUsageSession(stream, fullID)
	applyUsageEventTokenTotals(sess, sess.UsageEvents)
	return sess, msgs, true
}

func junieLinkSubagentCalls(
	results []ParseResult,
	streams map[string]*junieAgentStream,
	emitted map[string]bool,
	rawID string,
) {
	if len(results) == 0 {
		return
	}
	var subIDs []string
	for agentID := range emitted {
		subIDs = append(subIDs, agentID)
	}
	sort.Strings(subIDs)

	linkedSubagents := make(map[string]bool)
	for i := range results[0].Messages {
		for j := range results[0].Messages[i].ToolCalls {
			call := &results[0].Messages[i].ToolCalls[j]
			if call.Category != "Task" || call.SubagentSessionID != "" {
				continue
			}
			callStepID := call.ToolUseID
			if idx := strings.Index(call.ToolUseID, ":"); idx >= 0 {
				callStepID = call.ToolUseID[idx+1:]
			}
			callPrefix := strings.TrimSuffix(strings.TrimSuffix(callStepID, "-request"), "-spawned")
			callTask := strings.TrimSpace(gjson.Get(call.InputJSON, "task").Str)

			matchedAgent := ""
			if callPrefix != "" {
				// 1. Exact match on stepID or agentID
				for _, agentID := range subIDs {
					if linkedSubagents[agentID] {
						continue
					}
					st := streams[agentID]
					if st != nil && (st.stepID == callPrefix || agentID == callPrefix || agentID == callStepID) {
						matchedAgent = agentID
						break
					}
				}
				// 2. Delimited prefix match
				if matchedAgent == "" {
					for _, agentID := range subIDs {
						if linkedSubagents[agentID] {
							continue
						}
						st := streams[agentID]
						if st != nil && st.stepID != "" {
							if strings.HasPrefix(callPrefix, st.stepID+"-") ||
								strings.HasPrefix(callPrefix, st.stepID+"_") ||
								strings.HasPrefix(st.stepID, callPrefix+"-") ||
								strings.HasPrefix(st.stepID, callPrefix+"_") {
								matchedAgent = agentID
								break
							}
						}
					}
				}
			}
			callTS := results[0].Messages[i].Timestamp
			if len(call.ResultEvents) > 0 && !call.ResultEvents[0].Timestamp.IsZero() {
				callTS = call.ResultEvents[0].Timestamp
			}
			closestCandidate := func(cands []string) string {
				if len(cands) == 0 {
					return ""
				}
				if len(cands) == 1 {
					return cands[0]
				}
				bestIdx := 0
				bestDiff := time.Duration(1<<62 - 1)
				for idx, cand := range cands {
					st := streams[cand]
					stTime := st.firstTS
					if stTime.IsZero() {
						stTime = st.requestTS
					}
					var diff time.Duration
					if !callTS.IsZero() && !stTime.IsZero() {
						if stTime.After(callTS) {
							diff = stTime.Sub(callTS)
						} else {
							diff = callTS.Sub(stTime)
						}
					}
					if diff < bestDiff {
						bestDiff = diff
						bestIdx = idx
					}
				}
				return cands[bestIdx]
			}

			// 3. Fallback: match by task prompt content
			if matchedAgent == "" && callTask != "" {
				var exactCandidates []string
				var partialCandidates []string
				for _, agentID := range subIDs {
					if linkedSubagents[agentID] {
						continue
					}
					st := streams[agentID]
					if st != nil && st.request != "" {
						reqTrim := strings.TrimSpace(st.request)
						if reqTrim == callTask {
							exactCandidates = append(exactCandidates, agentID)
						} else if strings.Contains(reqTrim, callTask) || strings.Contains(callTask, reqTrim) {
							partialCandidates = append(partialCandidates, agentID)
						}
					}
				}
				candidates := exactCandidates
				if len(candidates) == 0 {
					candidates = partialCandidates
				}
				matchedAgent = closestCandidate(candidates)
			}
			// 4. Fallback: name match disambiguated by timestamp proximity
			if matchedAgent == "" && call.ToolName != "" {
				var candidates []string
				for _, agentID := range subIDs {
					if linkedSubagents[agentID] {
						continue
					}
					st := streams[agentID]
					if st != nil && (st.name == call.ToolName || st.taskName == call.ToolName) {
						candidates = append(candidates, agentID)
					}
				}
				matchedAgent = closestCandidate(candidates)
			}
			// 5. Fallback: single unlinked candidate when exactly 1 subagent exists
			if matchedAgent == "" && len(subIDs) == 1 && !linkedSubagents[subIDs[0]] {
				matchedAgent = subIDs[0]
			}

			if matchedAgent != "" {
				linkedSubagents[matchedAgent] = true
				call.SubagentSessionID = "junie:" + rawID + "--" + matchedAgent
				st := streams[matchedAgent]
				if st != nil {
					var status, content string
					for _, n := range st.notes {
						if n.SourceSubtype == "CancelAgentEvent" {
							status = "cancelled"
							content = n.Content
							break
						} else if n.SourceSubtype == "AgentFailureEvent" || n.SourceSubtype == "AgentTaskFailedEvent" {
							status = "error"
							content = n.Content
							break
						}
					}
					if status == "" {
						if st.cancelled || st.state == "CANCELLED" || st.state == "CANCELED" {
							status = "cancelled"
							content = "Subagent cancelled"
						} else if st.failed || st.state == "FAILED" {
							status = "error"
							content = "Subagent failed"
						} else {
							for _, b := range st.blocks {
								if b.cancelled || b.status == "CANCELLED" || b.status == "CANCELED" {
									status = "cancelled"
									content = "Subagent cancelled"
									break
								} else if b.status == "FAILED" || b.status == "ERROR" {
									status = "error"
									content = "Subagent failed"
									break
								}
							}
						}
					}
					if status != "" {
						if len(call.ResultEvents) == 0 {
							call.ResultEvents = []ParsedToolResultEvent{{
								Status:    status,
								Content:   content,
								Timestamp: st.lastTS,
							}}
						} else {
							call.ResultEvents[0].Status = status
							if call.ResultEvents[0].Content == "" {
								call.ResultEvents[0].Content = content
							}
						}
					}
				}
			}
		}
	}
}

func junieBaseRawSessionID(rawID string) string {
	if idx := strings.Index(rawID, "--"); idx > 0 {
		return rawID[:idx]
	}
	return rawID
}

// junieSessionIDSourceFiles reconstructs candidate events.jsonl paths from a
// raw session ID (with or without the "--agent-N" subagent suffix).
func junieSessionIDSourceFiles(roots []string, rawID string) []string {
	base := junieBaseRawSessionID(rawID)
	if !IsValidSessionID(base) {
		return nil
	}
	candidates := make([]string, 0, len(roots))
	for _, root := range roots {
		candidates = append(candidates,
			filepath.Join(root, base, "events.jsonl"))
	}
	return candidates
}
