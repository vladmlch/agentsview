package parser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.kenn.io/agentsview/internal/money"
)

const (
	junieEventsFile  = "events.jsonl"
	junieStateFile   = "state.json"
	junieSummaryFile = "summary.json"
)

type junieSessionSummary struct {
	projectDir string
	taskName   string
	lifecycle  string
	createdAt  time.Time
	updatedAt  time.Time
}

func (s junieIndexSummary) sessionSummary() junieSessionSummary {
	return junieSessionSummary{
		projectDir: s.ProjectDir,
		taskName:   s.TaskName,
		createdAt:  junieMillisTimestamp(s.CreatedAt),
		updatedAt:  junieMillisTimestamp(s.UpdatedAt),
	}
}

type junieTranscriptMessage struct {
	ParsedMessage
	active bool
	// seq is the transcript line number that produced this message; it orders
	// assembled output by file position, which stays a strict total order even
	// when timestamps are missing or equal.
	seq int
	// opensTurn marks a user prompt that starts a new agent turn: assistant
	// blocks written after it belong to a separate message even when they
	// share the taskID of earlier output.
	opensTurn bool
}

type junieBlock struct {
	kind      string
	stepID    string
	seq       int
	ts        time.Time
	taskID    string
	status    string
	text      string
	details   string
	toolType  string
	toolName  string
	command   string
	output    string
	input     string
	skillName string
	name      string
	model     string
	task      string
	patch     string
	title     string
	files     []string
	changes   []string
	options   []string
	cancelled bool
}

type junieAgentStream struct {
	id      string
	name    string
	kind    string
	stepID  string
	stepIDs map[string]bool
	// stepIDCanonical reports whether stepID came from a -request/-spawned
	// marker step rather than a bare stepId; once set, later markers must not
	// reseat the stream's canonical step.
	stepIDCanonical bool
	blocks          map[string]*junieBlock
	blockOrd        []string
	request         string
	requestTS       time.Time
	requestSeq      int
	usage           []ParsedUsageEvent
	cwd             string
	taskName        string
	firstTS         time.Time
	lastTS          time.Time
	notes           []junieTranscriptMessage
	cancelled       bool
	failed          bool
	state           string
}

func newJunieAgentStream(id string) *junieAgentStream {
	return &junieAgentStream{
		id:     id,
		blocks: make(map[string]*junieBlock),
	}
}

type junieParserState struct {
	entries          []junieTranscriptMessage
	userMessages     map[string]int
	streams          map[string]*junieAgentStream
	streamOrder      []string
	lastTaskState    string
	sourceSessionID  string
	startedAt        time.Time
	endedAt          time.Time
	sessionName      string
	sessionNameFound bool
	malformedLines   int
	lastLineBad      bool
}

type junieRootOpener func(string) (*os.Root, error)

func openJunieRoot(path string) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("junie root is not a directory")
	}
	return os.OpenRoot(path)
}

func openJunieEventStream(path string, openRoot junieRootOpener) (*os.File, error) {
	path = filepath.Clean(path)
	sessionDir := filepath.Dir(path)
	root, err := openRoot(filepath.Dir(sessionDir))
	if err != nil {
		return nil, err
	}
	defer root.Close()

	sessionName := filepath.Base(sessionDir)
	dirInfo, err := root.Lstat(sessionName)
	if err != nil {
		return nil, err
	}
	if !dirInfo.IsDir() {
		return nil, errors.New("session directory is not a directory")
	}

	relativePath := filepath.Join(sessionName, filepath.Base(path))
	info, err := root.Lstat(relativePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("session event stream is not a regular file")
	}

	f, err := openJuniePinnedFile(root, relativePath, info)
	if err != nil {
		return nil, fmt.Errorf("session event stream: %w", err)
	}
	return f, nil
}

func openJuniePinnedFile(root *os.Root, name string, expected os.FileInfo) (*os.File, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(expected, openedInfo) {
		_ = f.Close()
		return nil, errors.New("file changed while opening")
	}
	return f, nil
}

func parseJunieSessionWithSummary(
	ctx context.Context, path, machine string,
	summary junieSessionSummary, summaryPresent bool,
	openRoot junieRootOpener,
) ([]ParseResult, []string, error) {
	f, err := openJunieEventStream(path, openRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	rawID := filepath.Base(filepath.Dir(path))
	sessionDir := filepath.Dir(path)
	if err := junieLoadSidecarMeta(sessionDir, openRoot, &summary); err != nil {
		return nil, nil, fmt.Errorf("reading Junie sidecar metadata %s: %w", sessionDir, err)
	}
	stateCwd, stateTaskName := junieStateMeta(sessionDir, openRoot)

	state := junieParserState{
		userMessages:    make(map[string]int),
		streams:         map[string]*junieAgentStream{"main": newJunieAgentStream("main")},
		sourceSessionID: rawID,
	}
	lr := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(lr)
	for lineNumber := 1; ; lineNumber++ {
		line, ok := lr.next()
		if !ok {
			break
		}
		if err := contextErrEvery(ctx, lineNumber); err != nil {
			return nil, nil, err
		}
		if !gjson.Valid(line) {
			state.malformedLines++
			state.lastLineBad = true
			continue
		}
		state.lastLineBad = false
		if err := state.consumeEvent(gjson.Parse(line), lineNumber); err != nil {
			return nil, nil, fmt.Errorf("parsing Junie session %s line %d: %w", path, lineNumber, err)
		}
	}
	if err := lr.Err(); err != nil {
		return nil, nil, fmt.Errorf("reading Junie session %s: %w", path, err)
	}
	if lr.skippedOversized {
		return nil, nil, fmt.Errorf("reading Junie session %s: record exceeds %d bytes", path, maxLineSize)
	}
	return state.results(
		ctx, path, machine, rawID, info, summary, summaryPresent, stateCwd, stateTaskName,
	)
}

func (s *junieParserState) consumeEvent(event gjson.Result, lineNumber int) error {
	timestamp := junieTimestamp(event.Get("timestampMs"))
	if !timestamp.IsZero() {
		if s.startedAt.IsZero() || timestamp.Before(s.startedAt) {
			s.startedAt = timestamp
		}
		if timestamp.After(s.endedAt) {
			s.endedAt = timestamp
		}
	}

	switch event.Get("kind").Str {
	case "UserPromptEvent":
		s.consumeUserPrompt(event, timestamp, lineNumber)
	case "UserResponseEvent":
		s.appendMessage(RoleUser, event.Get("prompt").Str, timestamp, lineNumber)
	case "UserAsyncResponseEvent":
		s.consumeAsyncResponse(event, timestamp, lineNumber)
	case "UserMessagesCommittedToHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), true)
	case "UserMessagesDroppedFromHistory", "UserMessagesFailedInHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), false)
	case "SessionTitleSetEvent":
		s.sessionName = event.Get("name").Str
		s.sessionNameFound = true
	case "SystemMessageEvent":
		s.consumeSystemMessage(event, timestamp, lineNumber)
	case "CancelAgentEvent":
		s.consumeAgentLifecycleEvent(event, timestamp, "CancelAgentEvent", lineNumber)
	case "AgentTaskFailedEvent", "AgentFailureEvent":
		s.consumeAgentLifecycleEvent(event, timestamp, event.Get("kind").Str, lineNumber)
	case "TaskState":
		st := strings.ToUpper(event.Get("state").Str)
		if !junieTerminalTaskState(s.lastTaskState) || junieTerminalTaskState(st) {
			s.lastTaskState = st
		}
	case "SessionA2uxEvent":
		return s.consumeA2UXEvent(event, timestamp, lineNumber)
	}
	return nil
}

// junieTerminalTaskState reports whether a task state is terminal-failed:
// once reached, it must not be overwritten by a resumed in-flight state.
func junieTerminalTaskState(state string) bool {
	return state == "CANCELLED" || state == "CANCELED" || state == "FAILED"
}

func (s *junieParserState) consumeUserPrompt(
	event gjson.Result, timestamp time.Time, lineNumber int,
) {
	content := firstNonEmptyJSONLString(
		event.Get("presentablePrompt").Str,
		event.Get("prompt").Str,
	)
	requestID := event.Get("requestId").Str
	active := event.Get("delivery").Str != "Failed" && strings.TrimSpace(content) != ""
	if index, ok := s.userMessages[requestID]; requestID != "" && ok {
		s.entries[index].Content = content
		s.entries[index].ContentLength = len(content)
		s.entries[index].Timestamp = timestamp
		s.entries[index].active = active
		s.entries[index].SourceUUID = requestID
		s.entries[index].opensTurn = true
		return
	}
	index := s.appendMessage(RoleUser, content, timestamp, lineNumber)
	s.entries[index].active = active
	s.entries[index].SourceUUID = requestID
	s.entries[index].opensTurn = true
	if requestID != "" {
		s.userMessages[requestID] = index
	}
}

func (s *junieParserState) consumeAsyncResponse(
	event gjson.Result, timestamp time.Time, lineNumber int,
) {
	var responses []string
	for _, entry := range event.Get("entries").Array() {
		response := strings.TrimSpace(strings.Join([]string{
			entry.Get("question").Str,
			entry.Get("answer").Str,
		}, "\n"))
		if response != "" {
			responses = append(responses, response)
		}
	}
	s.appendMessage(RoleUser, strings.Join(responses, "\n\n"), timestamp, lineNumber)
}

func (s *junieParserState) consumeSystemMessage(
	event gjson.Result, timestamp time.Time, lineNumber int,
) {
	text := strings.TrimSpace(event.Get("text").Str)
	details := strings.TrimSpace(event.Get("details").Str)
	if details != "" && details != text {
		text = strings.TrimSpace(text + "\n\n" + details)
	}
	s.entries = append(s.entries, junieTranscriptMessage{
		ParsedMessage: ParsedMessage{
			Role:          RoleSystem,
			IsSystem:      true,
			Content:       text,
			ContentLength: len(text),
			Timestamp:     timestamp,
			SourceSubtype: event.Get("level").Str,
		},
		active: strings.TrimSpace(text) != "",
		seq:    lineNumber,
	})
}

func (s *junieParserState) consumeAgentLifecycleEvent(
	event gjson.Result, timestamp time.Time, kind string, lineNumber int,
) {
	targetAgent := firstNonEmptyJSONLString(
		event.Get("agent.id").Str, event.Get("agentId").Str,
		event.Get("targetAgentId").Str, event.Get("agent.name").Str,
		event.Get("agentName").Str,
	)
	stepID := event.Get("stepId").Str
	fallback := "Task failed"
	if kind == "CancelAgentEvent" {
		fallback = "Task cancelled"
	}
	text := firstNonEmptyJSONLString(
		event.Get("message").Str, event.Get("reason").Str,
		event.Get("errorCode").Str, fallback,
	)
	msg := junieTranscriptMessage{
		ParsedMessage: ParsedMessage{
			Role:          RoleSystem,
			IsSystem:      true,
			Content:       text,
			ContentLength: len(text),
			Timestamp:     timestamp,
			SourceSubtype: kind,
		},
		active: strings.TrimSpace(text) != "",
		seq:    lineNumber,
	}
	st := findJunieStream(s.streams, targetAgent, stepID)
	switch {
	case st != nil && st.id != "main":
		if kind == "CancelAgentEvent" {
			st.cancelled = true
			st.state = "CANCELLED"
		} else {
			st.failed = true
			st.state = "FAILED"
		}
		st.notes = append(st.notes, msg)
	case targetAgent != "" && targetAgent != "main":
		st = newJunieAgentStream(targetAgent)
		if kind == "CancelAgentEvent" {
			st.cancelled = true
			st.state = "CANCELLED"
		} else {
			st.failed = true
			st.state = "FAILED"
		}
		s.streams[targetAgent] = st
		s.streamOrder = append(s.streamOrder, targetAgent)
		st.notes = append(st.notes, msg)
	default:
		if kind == "CancelAgentEvent" {
			s.lastTaskState = "CANCELLED"
		} else {
			s.lastTaskState = "FAILED"
		}
		s.entries = append(s.entries, msg)
	}
}

func (s *junieParserState) consumeA2UXEvent(
	event gjson.Result, timestamp time.Time, lineNumber int,
) error {
	ae := event.Get("event.agentEvent")
	kind := ae.Get("kind").Str
	agentID := ae.Get("agent.id").Str
	if agentID == "" {
		agentID = ae.Get("agentId").Str
	}
	if agentID == "" {
		if ae.Get("agent.kind").Str == "BuiltInAgent" {
			// Only a -request/-spawned marker step identifies the agent: a
			// bare stepId would mint a stream per step and split one agent's
			// output across many sessions.
			agentID = firstNonEmptyJSONLString(
				ae.Get("agent.name").Str,
				junieStepMarkerID(ae.Get("stepId").Str),
				"subagent",
			)
		} else {
			agentID = "main"
		}
	}
	stream := s.streams[agentID]
	if stream == nil {
		stream = newJunieAgentStream(agentID)
		s.streams[agentID] = stream
		if agentID != "main" {
			s.streamOrder = append(s.streamOrder, agentID)
		}
	}
	if stream.name == "" {
		stream.name = firstNonEmptyJSONLString(
			ae.Get("agent.name").Str, ae.Get("name").Str)
	}
	if stream.kind == "" {
		stream.kind = ae.Get("agent.kind").Str
	}
	if !timestamp.IsZero() {
		if stream.firstTS.IsZero() || timestamp.Before(stream.firstTS) {
			stream.firstTS = timestamp
		}
		if timestamp.After(stream.lastTS) {
			stream.lastTS = timestamp
		}
	}
	if agentID != "main" {
		if rawStepID := ae.Get("stepId").Str; rawStepID != "" {
			if stream.stepIDs == nil {
				stream.stepIDs = make(map[string]bool)
			}
			stream.stepIDs[rawStepID] = true
			trimmed := junieTrimStepMarker(rawStepID)
			isMarker := trimmed != rawStepID
			if isMarker {
				stream.stepIDs[trimmed] = true
			}
			if stream.stepID == "" || (isMarker && !stream.stepIDCanonical) {
				// A marker-derived canonical step overrides a bare first-seen
				// stepId; once canonical, later markers must not reseat it.
				stream.stepID = trimmed
				stream.stepIDCanonical = isMarker
			}
		}
	}
	taskID := event.Get("taskId").Str
	eventState := strings.ToUpper(event.Get("event.state").Str)
	if eventState == "CANCELLED" || eventState == "CANCELED" {
		if agentID == "main" {
			s.lastTaskState = "CANCELLED"
		} else {
			stream.cancelled = true
			stream.state = "CANCELLED"
		}
	} else if eventState == "FAILED" || eventState == "ERROR" {
		if agentID == "main" {
			if !junieTerminalTaskState(s.lastTaskState) {
				s.lastTaskState = "FAILED"
			}
		} else {
			stream.failed = true
			stream.state = "FAILED"
		}
	}

	switch kind {
	case "AgentThoughtBlockUpdatedEvent",
		"ResultBlockUpdatedEvent",
		"MarkdownBlockUpdatedEvent",
		"ToolBlockUpdatedEvent",
		"TerminalBlockUpdatedEvent",
		"ViewFilesBlockUpdatedEvent",
		"FileChangesBlockUpdatedEvent",
		"McpBlockUpdatedEvent",
		"AgentPatchCreatedEvent",
		"AskAsyncRequestUpdatedEvent",
		"ChoiceRequestUpdatedEvent":
		junieUpsertBlock(stream, ae, kind, taskID, eventState, lineNumber, timestamp)
	case "CustomAgentBlockUpdatedEvent",
		"SubagentSpawnedEvent":
		// A non-main agent spawning its own subagent still records the call in
		// that agent's stream; dropping it would lose the child spawn entirely.
		junieUpsertBlock(stream, ae, kind, taskID, eventState, lineNumber, timestamp)
	case "AgentTaskNameUpdatedEvent":
		name := strings.TrimSpace(firstNonEmptyJSONLString(
			ae.Get("name").Str, ae.Get("taskName").Str))
		if agentID == "main" {
			if name != "" {
				s.sessionName = name
				s.sessionNameFound = true
			}
		} else if stream.taskName == "" && name != "" {
			stream.taskName = name
		}
	case "LlmResponseMetadataEvent":
		return s.consumeModelUsage(stream, ae, taskID, timestamp, lineNumber)
	case "CurrentDirectoryUpdatedEvent":
		if dir := ae.Get("currentDirectory").Str; dir != "" && stream.cwd == "" {
			stream.cwd = dir
		}
	case "AgentFailureEvent", "AgentTaskFailedEvent", "CancelAgentEvent":
		fallback := "Task failed"
		if kind == "CancelAgentEvent" {
			fallback = "Task cancelled"
		}
		text := firstNonEmptyJSONLString(
			ae.Get("message").Str, ae.Get("reason").Str,
			ae.Get("errorCode").Str, fallback,
		)
		stream.notes = append(stream.notes, junieTranscriptMessage{
			ParsedMessage: ParsedMessage{
				Role:          RoleSystem,
				IsSystem:      true,
				Content:       text,
				ContentLength: len(text),
				Timestamp:     timestamp,
				SourceSubtype: kind,
			},
			active: strings.TrimSpace(text) != "",
			seq:    lineNumber,
		})
		if kind == "CancelAgentEvent" {
			if agentID == "main" {
				s.lastTaskState = "CANCELLED"
			} else {
				stream.cancelled = true
				stream.state = "CANCELLED"
			}
		} else {
			if agentID == "main" {
				if !junieTerminalTaskState(s.lastTaskState) {
					s.lastTaskState = "FAILED"
				}
			} else {
				stream.failed = true
				stream.state = "FAILED"
			}
		}
	default:
	}
	return nil
}

func junieUpsertBlock(
	stream *junieAgentStream, ae gjson.Result, kind, taskID, eventState string,
	seq int, ts time.Time,
) {
	stepID := ae.Get("stepId").Str
	if stepID == "" {
		stepID = kind + "#" + ae.Raw[:min(len(ae.Raw), 64)]
	}
	status := strings.ToUpper(firstNonEmptyJSONLString(ae.Get("status").Str, eventState))
	cancelled := ae.Get("cancelled").Bool() ||
		status == "CANCELLED" ||
		status == "CANCELED" ||
		eventState == "CANCELLED" ||
		eventState == "CANCELED" ||
		(stream != nil && stream.cancelled)

	text := firstNonEmptyJSONLString(ae.Get("text").Str, ae.Get("result").Str)
	if kind == "ResultBlockUpdatedEvent" {
		text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "<!-- ANSWER -->"))
	}

	b := &junieBlock{
		kind:      kind,
		stepID:    stepID,
		seq:       seq,
		ts:        ts,
		taskID:    taskID,
		status:    status,
		text:      text,
		details:   ae.Get("details").Str,
		toolType:  ae.Get("toolType").Str,
		toolName:  ae.Get("toolName").Str,
		command:   ae.Get("command").Str,
		output:    ae.Get("output").Str,
		input:     ae.Get("input").Str,
		skillName: ae.Get("skillName").Str,
		name:      firstNonEmptyJSONLString(ae.Get("name").Str, ae.Get("agent.name").Str),
		model:     ae.Get("model").Str,
		task:      firstNonEmptyJSONLString(ae.Get("task").Str, ae.Get("description").Str, ae.Get("prompt").Str),
		patch:     ae.Get("patch").Str,
		title:     firstNonEmptyJSONLString(ae.Get("title").Str, ae.Get("request.question").Str, ae.Get("request.name").Str),
		cancelled: cancelled,
	}
	for _, f := range ae.Get("files").Array() {
		if p := f.Get("relativePath").Str; p != "" {
			b.files = append(b.files, p)
		}
	}
	for _, c := range ae.Get("changes").Array() {
		if p := c.Get("afterRelativePath").Str; p != "" {
			b.changes = append(b.changes, p)
		} else if p := c.Get("relativePath").Str; p != "" {
			b.changes = append(b.changes, p)
		}
	}
	for _, o := range ae.Get("request.options").Array() {
		if t := o.Get("title").Str; t != "" {
			b.options = append(b.options, t)
		}
	}
	if kind == "MarkdownBlockUpdatedEvent" &&
		strings.HasSuffix(stepID, "-request") && stream.id != "main" {
		if stream.request == "" {
			stream.request = b.text
			stream.requestTS = ts
			stream.requestSeq = seq
		}
		if stream.stepIDs == nil {
			stream.stepIDs = make(map[string]bool)
		}
		stream.stepIDs[stepID] = true
		stream.stepIDs[strings.TrimSuffix(stepID, "-request")] = true
		if stream.stepID == "" || !stream.stepIDCanonical {
			stream.stepID = strings.TrimSuffix(stepID, "-request")
			stream.stepIDCanonical = true
		}
		return
	}
	if existing, ok := stream.blocks[stepID]; ok {
		b.seq = existing.seq
		stream.blocks[stepID] = b
		return
	}
	stream.blocks[stepID] = b
	stream.blockOrd = append(stream.blockOrd, stepID)
}

func (s *junieParserState) consumeModelUsage(
	stream *junieAgentStream, event gjson.Result, taskID string, timestamp time.Time, lineNumber int,
) error {
	for index, usage := range event.Get("modelUsage").Array() {
		parsed := ParsedUsageEvent{
			Source:                   "llm-response",
			Model:                    strings.TrimSpace(usage.Get("model").Str),
			InputTokens:              max(int(usage.Get("inputTokens").Int()), 0),
			OutputTokens:             max(int(usage.Get("outputTokens").Int()), 0),
			CacheCreationInputTokens: max(int(usage.Get("cacheCreateTokens").Int()), 0),
			CacheReadInputTokens:     max(int(usage.Get("cacheInputTokens").Int()), 0),
			OccurredAt:               timeString(timestamp.UTC(), s.startedAt.UTC()),
			DedupKey: fmt.Sprintf(
				"llm-response:%d:%d", lineNumber, index,
			),
		}
		costValue := usage.Get("cost")
		if costValue.Exists() && costValue.Type != gjson.Null {
			if costValue.Type != gjson.Number || costValue.Num < 0 {
				return errors.New("invalid model usage cost")
			}
			cost, err := money.ParseDollars(costValue.Raw)
			if err != nil {
				return fmt.Errorf("parsing model usage cost: %w", err)
			}
			parsed.Cost = &cost
			parsed.CostStatus = "exact"
			parsed.CostSource = "junie-model-usage"
		}
		if parsed.InputTokens > 0 || parsed.OutputTokens > 0 ||
			parsed.CacheCreationInputTokens > 0 || parsed.CacheReadInputTokens > 0 ||
			parsed.Cost != nil {
			stream.usage = append(stream.usage, parsed)
		}
	}
	return nil
}

// junieTrimStepMarker strips the synthetic -request/-spawned marker suffixes
// that identify a subagent's canonical step.
func junieTrimStepMarker(stepID string) string {
	return strings.TrimSuffix(strings.TrimSuffix(stepID, "-request"), "-spawned")
}

// junieStepMarkerID returns the canonical step only when a marker suffix was
// actually present, so a bare stepId is not mistaken for an agent identifier.
func junieStepMarkerID(stepID string) string {
	if trimmed := junieTrimStepMarker(stepID); trimmed != stepID {
		return trimmed
	}
	return ""
}

func junieRewriteUsageSession(
	stream *junieAgentStream, fullID string,
) []ParsedUsageEvent {
	out := make([]ParsedUsageEvent, len(stream.usage))
	copy(out, stream.usage)
	for i := range out {
		out[i].SessionID = fullID
		out[i].DedupKey = fullID + ":" + out[i].DedupKey
	}
	return out
}

func junieAssembleMessages(
	stream *junieAgentStream, extra []junieTranscriptMessage,
) []ParsedMessage {
	type sequencedMessage struct {
		seq int
		msg ParsedMessage
	}

	var taskIDs []string
	byTask := make(map[string][]*junieBlock)
	for _, stepID := range stream.blockOrd {
		b := stream.blocks[stepID]
		if b == nil {
			continue
		}
		if _, seen := byTask[b.taskID]; !seen {
			taskIDs = append(taskIDs, b.taskID)
		}
		byTask[b.taskID] = append(byTask[b.taskID], b)
	}

	// A user prompt whose transcript line falls between two blocks of one
	// task splits the task's assistant output into separate messages:
	// content written after the prompt must not surface ahead of it. Inline
	// question answers and lifecycle notes do not open a new turn.
	cuts := make([]int, 0, len(extra))
	for _, entry := range extra {
		if entry.opensTurn {
			cuts = append(cuts, entry.seq)
		}
	}
	sort.Ints(cuts)

	var merged []sequencedMessage
	for _, entry := range extra {
		merged = append(merged, sequencedMessage{seq: entry.seq, msg: entry.ParsedMessage})
	}
	for _, taskID := range taskIDs {
		blocks := byTask[taskID]
		sort.SliceStable(blocks, func(i, j int) bool {
			return blocks[i].seq < blocks[j].seq
		})
		runStart := 0
		for i := 1; i <= len(blocks); i++ {
			if i < len(blocks) && !junieSeqBetween(cuts, blocks[i-1].seq, blocks[i].seq) {
				continue
			}
			if msg, ok := junieAssistantMessage(blocks[runStart:i], stream); ok {
				merged = append(merged, sequencedMessage{
					seq: blocks[runStart].seq, msg: msg,
				})
			}
			runStart = i
		}
	}
	for _, note := range stream.notes {
		merged = append(merged, sequencedMessage{seq: note.seq, msg: note.ParsedMessage})
	}

	// Order by file position (the transcript line that produced each
	// element). Unlike timestamps it is a strict total order — line numbers
	// are never missing or equal — and it preserves the file's interleaving
	// of user prompts, assistant output, and lifecycle notes even when
	// timestampMs is absent.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].seq < merged[j].seq
	})
	messages := make([]ParsedMessage, len(merged))
	for i := range merged {
		messages[i] = merged[i].msg
		messages[i].Ordinal = i
	}
	return messages
}

// junieSeqBetween reports whether a sorted boundary list holds a position
// strictly between two transcript lines.
func junieSeqBetween(cuts []int, lo, hi int) bool {
	i := sort.SearchInts(cuts, lo+1)
	return i < len(cuts) && cuts[i] < hi
}

func junieAssistantMessage(
	blocks []*junieBlock, stream *junieAgentStream,
) (ParsedMessage, bool) {
	var thinking, content []string
	var calls []ParsedToolCall
	var ts time.Time

	for _, b := range blocks {
		if ts.IsZero() || (!b.ts.IsZero() && b.ts.Before(ts)) {
			ts = b.ts
		}
		switch b.kind {
		case "AgentThoughtBlockUpdatedEvent":
			if b.text != "" {
				thinking = append(thinking, b.text)
			}
		case "ResultBlockUpdatedEvent", "MarkdownBlockUpdatedEvent":
			if b.text != "" && !b.cancelled {
				content = append(content, b.text)
			}
		default:
			if call, ok := junieToolCall(b); ok {
				calls = append(calls, call)
			}
		}
	}

	if len(thinking) == 0 && len(content) == 0 && len(calls) == 0 {
		return ParsedMessage{}, false
	}
	msg := ParsedMessage{
		Role:         RoleAssistant,
		Content:      strings.Join(content, "\n\n"),
		ThinkingText: strings.Join(thinking, "\n\n"),
		Timestamp:    ts,
		HasThinking:  len(thinking) > 0,
		HasToolUse:   len(calls) > 0,
		ToolCalls:    calls,
	}
	msg.ContentLength = len(msg.Content)
	return msg, true
}

func junieToolCall(b *junieBlock) (ParsedToolCall, bool) {
	status := junieResultStatus(b.status)
	if b.cancelled && (status == "" || status == "running") {
		status = "cancelled"
	}
	event := func(content string) []ParsedToolResultEvent {
		if status == "" && content == "" {
			return nil
		}
		return []ParsedToolResultEvent{{
			Status:    status,
			Content:   content,
			Timestamp: b.ts,
		}}
	}
	completed := func(content string) []ParsedToolResultEvent {
		return []ParsedToolResultEvent{{
			Status:    "completed",
			Content:   content,
			Timestamp: b.ts,
		}}
	}
	input := func(fields map[string]any) string {
		if len(fields) == 0 {
			return ""
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return ""
		}
		return string(raw)
	}

	switch b.kind {
	case "TerminalBlockUpdatedEvent":
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     "Terminal",
			Category:     "Bash",
			InputJSON:    input(map[string]any{"command": b.command}),
			ResultEvents: event(b.output),
		}, true
	case "ViewFilesBlockUpdatedEvent":
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     "ViewFiles",
			Category:     "Read",
			InputJSON:    input(map[string]any{"files": b.files}),
			FilePath:     firstString(b.files),
			ResultEvents: event(b.details),
		}, true
	case "FileChangesBlockUpdatedEvent":
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     "FileChanges",
			Category:     "Edit",
			InputJSON:    input(map[string]any{"files": b.changes}),
			FilePath:     firstString(b.changes),
			ResultEvents: event(b.details),
		}, true
	case "McpBlockUpdatedEvent":
		inputJSON := strings.TrimSpace(b.input)
		if inputJSON != "" && !json.Valid([]byte(inputJSON)) {
			// Some MCP blocks carry raw text instead of a JSON payload; wrap it
			// so InputJSON stays a valid JSON object for storage and UI decode.
			inputJSON = input(map[string]any{"input": b.input})
		}
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     b.toolName,
			Category:     "Tool",
			InputJSON:    inputJSON,
			ResultEvents: event(b.details),
		}, true
	case "ToolBlockUpdatedEvent":
		category := "Other"
		switch b.toolType {
		case "Search":
			category = "Grep"
		case "Skill":
			category = "Tool"
		}
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     firstNonEmptyJSONLString(b.toolType, "Tool"),
			Category:     category,
			InputJSON:    input(map[string]any{"text": b.text}),
			SkillName:    b.skillName,
			ResultEvents: event(b.details),
		}, true
	case "CustomAgentBlockUpdatedEvent", "SubagentSpawnedEvent":
		var events []ParsedToolResultEvent
		switch {
		case b.cancelled:
			events = []ParsedToolResultEvent{{
				Status:    "cancelled",
				Timestamp: b.ts,
			}}
		case b.status == "":
			events = completed("")
		default:
			// IN_PROGRESS/STARTED map to a "running" result event, keeping the
			// spawn call unresolved so the parent classifies as
			// tool_call_pending until the subagent reports a terminal state.
			events = event("")
		}
		return ParsedToolCall{
			ToolUseID: b.taskID + ":" + b.stepID,
			ToolName:  b.name,
			Category:  "Task",
			InputJSON: input(map[string]any{
				"task": b.task, "model": b.model,
			}),
			ResultEvents: events,
		}, true
	case "AgentPatchCreatedEvent":
		return ParsedToolCall{
			ToolUseID:    b.taskID + ":" + b.stepID,
			ToolName:     "Patch",
			Category:     "Edit",
			InputJSON:    input(map[string]any{"patch": b.patch}),
			ResultEvents: completed(b.patch),
		}, true
	case "AskAsyncRequestUpdatedEvent", "ChoiceRequestUpdatedEvent":
		return ParsedToolCall{
			ToolUseID: b.taskID + ":" + b.stepID,
			ToolName:  "AskUserQuestion",
			Category:  "Tool",
			InputJSON: input(map[string]any{
				"title": b.title, "options": b.options,
			}),
			ResultEvents: completed(""),
		}, true
	}
	return ParsedToolCall{}, false
}

func junieResultStatus(status string) string {
	switch status {
	case "COMPLETED", "FINISHED":
		return "completed"
	case "FAILED", "ERROR":
		return "error"
	case "CANCELLED", "CANCELED":
		return "cancelled"
	case "IN_PROGRESS", "STARTED":
		return "running"
	default:
		return strings.ToLower(status)
	}
}

func (s *junieParserState) appendMessage(
	role RoleType, content string, timestamp time.Time, seq int,
) int {
	s.entries = append(s.entries, junieTranscriptMessage{
		ParsedMessage: ParsedMessage{
			Role:          role,
			Content:       content,
			Timestamp:     timestamp,
			IsSystem:      role == RoleSystem,
			ContentLength: len(content),
		},
		active: strings.TrimSpace(content) != "",
		seq:    seq,
	})
	return len(s.entries) - 1
}

func (s *junieParserState) setUserMessagesActive(ids gjson.Result, active bool) {
	for _, id := range ids.Array() {
		if index, ok := s.userMessages[id.Str]; ok {
			s.entries[index].active = active
		}
	}
}

func (s *junieParserState) parentResult(
	ctx context.Context, path, machine, sourceSessionID string,
	fileSize, mtime int64,
	summary junieSessionSummary, summaryPresent bool,
	stateCwd, stateTaskName string,
) (*ParsedSession, []ParsedMessage, bool) {
	main := s.streams["main"]
	if main == nil {
		main = newJunieAgentStream("main")
	}
	var extra []junieTranscriptMessage
	userCount := 0
	firstMessage := ""
	for _, entry := range s.entries {
		if !entry.active || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		extra = append(extra, entry)
		if entry.Role == RoleUser && !entry.IsSystem {
			userCount++
			if firstMessage == "" {
				firstMessage = truncate(strings.ReplaceAll(entry.Content, "\n", " "), 300)
			}
		}
	}
	messages := junieAssembleMessages(main, extra)

	hasActivity := len(main.blocks) > 0 || len(main.usage) > 0
	// A wholly empty or unrecognized stream may be a partial rewrite and stays
	// skipped. An external name from index.jsonl/state.json must not rescue it
	// into a title-only session: with ForceReplace that would erase previously
	// stored messages. Recognized empty projections still emit so replacement
	// can clear stale rows.
	if len(messages) == 0 && len(s.entries) == 0 && !hasActivity && !s.sessionNameFound {
		return nil, nil, false
	}
	if userCount == 0 && !hasActivity && !s.sessionNameFound && !summaryPresent && summary.taskName == "" && stateTaskName == "" {
		return nil, nil, true
	}

	startedAt := s.startedAt
	if !summary.createdAt.IsZero() && (startedAt.IsZero() || summary.createdAt.Before(startedAt)) {
		startedAt = summary.createdAt
	}
	if startedAt.IsZero() {
		startedAt = junieTimestampFromID(sourceSessionID)
	}
	if startedAt.IsZero() && mtime > 0 {
		startedAt = time.Unix(0, mtime).UTC()
	}

	endedAt := s.endedAt
	if !summary.updatedAt.IsZero() && summary.updatedAt.After(endedAt) {
		endedAt = summary.updatedAt
	}
	if endedAt.IsZero() || endedAt.Before(startedAt) {
		if mtime > 0 && time.Unix(0, mtime).UTC().After(startedAt) {
			endedAt = time.Unix(0, mtime).UTC()
		} else {
			endedAt = startedAt
		}
	}

	if !s.sessionNameFound {
		if summary.taskName != "" {
			s.sessionName = summary.taskName
		} else if stateTaskName != "" {
			s.sessionName = stateTaskName
		}
	}
	if firstMessage == "" && strings.TrimSpace(s.sessionName) != "" {
		firstMessage = s.sessionName
	}

	projectDir := strings.TrimSpace(summary.projectDir)
	cwd := projectDir
	if cwd == "" {
		cwd = main.cwd
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

	for i := range messages {
		if messages[i].Timestamp.IsZero() {
			messages[i].Timestamp = startedAt
		}
	}

	usageEvents := junieRewriteUsageSession(main, "junie:"+sourceSessionID)

	session := &ParsedSession{
		ID:                 "junie:" + sourceSessionID,
		SourceSessionID:    sourceSessionID,
		Project:            project,
		Machine:            machine,
		Agent:              AgentJunie,
		Cwd:                cwd,
		FirstMessage:       firstMessage,
		SessionName:        s.sessionName,
		SessionNamePresent: s.sessionNameFound || summaryPresent || summary.taskName != "" || stateTaskName != "",
		StartedAt:          startedAt,
		EndedAt:            endedAt,
		MessageCount:       len(messages),
		UserMessageCount:   userCount,
		MalformedLines:     s.malformedLines,
		File: FileInfo{
			Path:  path,
			Size:  fileSize,
			Mtime: mtime,
		},
	}
	session.IsTruncated = s.lastLineBad
	stopReason := junieStopReason(s.lastTaskState, summary.lifecycle)
	isCancelledOrFailed := s.lastTaskState == "CANCELLED" || s.lastTaskState == "CANCELED" || s.lastTaskState == "FAILED" ||
		summary.lifecycle == "CANCELLED" || summary.lifecycle == "CANCELED" || summary.lifecycle == "FAILED"
	if isCancelledOrFailed {
		session.TerminationStatus = TerminationTruncated
	} else {
		session.TerminationStatus = Classify(messages, stopReason, s.lastLineBad)
	}
	applyUsageEventTokenTotals(session, usageEvents)
	session.UsageEvents = usageEvents
	return session, messages, false
}

// junieStopReason maps a terminal task state or sidecar lifecycle to the
// canonical stop reason used by termination classification.
func junieStopReason(lastTaskState, lifecycle string) string {
	if lastTaskState == "COMPLETED" ||
		lifecycle == "FINISHED" || lifecycle == "COMPLETED" {
		return "task_complete"
	}
	return ""
}

// foldOrphanStream merges a subagent stream that produced no session of its
// own back into the parent state: model usage joins the main stream and
// lifecycle notes become parent transcript entries so a failure or
// cancellation note is never silently dropped.
func (s *junieParserState) foldOrphanStream(stream *junieAgentStream) {
	if stream == nil {
		return
	}
	main := s.streams["main"]
	if main == nil {
		main = newJunieAgentStream("main")
		s.streams["main"] = main
	}
	main.usage = append(main.usage, stream.usage...)
	s.entries = append(s.entries, stream.notes...)
}

func (s *junieParserState) results(
	ctx context.Context, path, machine, rawID string, info os.FileInfo,
	summary junieSessionSummary, summaryPresent bool,
	stateCwd, stateTaskName string,
) ([]ParseResult, []string, error) {
	fullID := "junie:" + rawID
	var fileSize, mtime int64
	if info != nil {
		fileSize = info.Size()
		mtime = info.ModTime().UnixNano()
	}

	// Finish subagent streams before the parent: streams that produce no
	// session of their own (no request, no emitted blocks) fold their usage
	// and notes back into the parent result.
	type subOutcome struct {
		sess *ParsedSession
		msgs []ParsedMessage
	}
	var subs []subOutcome
	emitted := map[string]bool{}
	for _, agentID := range s.streamOrder {
		stream := s.streams[agentID]
		sub, subMsgs, ok := junieFinishSubagent(
			ctx, fullID+"--"+agentID, fullID, path, info,
			stream, summary, summaryPresent, machine, "",
			stateCwd, rawID+"--"+agentID,
		)
		if !ok {
			s.foldOrphanStream(stream)
			continue
		}
		emitted[agentID] = true
		subs = append(subs, subOutcome{sess: sub, msgs: subMsgs})
	}

	sess, msgs, isStub := s.parentResult(
		ctx, path, machine, rawID, fileSize, mtime, summary, summaryPresent, stateCwd, stateTaskName,
	)
	var results []ParseResult
	var excluded []string
	if sess == nil {
		if isStub {
			excluded = append(excluded, fullID)
		}
	} else {
		results = append(results, ParseResult{
			Session: *sess, Messages: msgs, UsageEvents: sess.UsageEvents,
		})
	}
	for _, sub := range subs {
		results = append(results, ParseResult{
			Session: *sub.sess, Messages: sub.msgs, UsageEvents: sub.sess.UsageEvents,
		})
	}
	if len(results) > 0 && results[0].Session.ID == fullID {
		junieLinkSubagentCalls(results, s.streams, emitted, rawID)
		if results[0].Session.TerminationStatus == TerminationToolCallPending {
			results[0].Session.TerminationStatus = Classify(
				results[0].Messages,
				junieStopReason(s.lastTaskState, summary.lifecycle),
				s.lastLineBad,
			)
		}
	}
	return results, excluded, nil
}

// errJunieSidecarNotRegular marks a sidecar (summary.json, state.json) that is
// present but not a regular file — a symlink, a directory, or a device node.
// Companion fingerprinting silently skips such files, so parsing must treat
// them as absent too; otherwise a clean fingerprint would be followed by a
// permanent parse failure.
var errJunieSidecarNotRegular = errors.New("junie sidecar is not a regular file")

func readJunieSidecarFile(
	sessionDir, name string, openRoot junieRootOpener,
) ([]byte, error) {
	root, err := openRoot(filepath.Dir(sessionDir))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	rel := filepath.Join(filepath.Base(sessionDir), name)
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errJunieSidecarNotRegular
	}
	f, err := openJuniePinnedFile(root, rel, info)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func junieLoadSidecarMeta(
	sessionDir string, openRoot junieRootOpener, summary *junieSessionSummary,
) error {
	raw, err := readJunieSidecarFile(sessionDir, junieSummaryFile, openRoot)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) ||
			errors.Is(err, errJunieSidecarNotRegular) {
			return nil
		}
		return err
	}
	if !gjson.ValidBytes(raw) {
		return nil
	}
	s := gjson.ParseBytes(raw)
	if v := s.Get("taskName").Str; v != "" {
		summary.taskName = v
	}
	if v := s.Get("projectDir").Str; v != "" {
		summary.projectDir = v
	}
	if v := firstNonEmptyJSONLString(s.Get("lifecycle").Str, s.Get("status").Str, s.Get("state").Str); v != "" {
		summary.lifecycle = strings.ToUpper(v)
	}
	if t := junieTimestamp(s.Get("createdAt")); !t.IsZero() {
		summary.createdAt = t
	}
	if t := junieTimestamp(s.Get("updatedAt")); !t.IsZero() {
		summary.updatedAt = t
	}
	return nil
}

func junieStateMeta(sessionDir string, openRoot junieRootOpener) (string, string) {
	raw, err := readJunieSidecarFile(sessionDir, junieStateFile, openRoot)
	if err != nil || !gjson.ValidBytes(raw) {
		return "", ""
	}
	blob := gjson.GetBytes(raw, "event.agentEvent.blob").Str
	if blob == "" || !gjson.Valid(blob) {
		return "", ""
	}
	cwd := gjson.Get(blob, "currentDirectory").Str
	taskName := gjson.Get(blob, "lastAgentState.issue.name").Str
	return cwd, taskName
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseJunieSessionSummary(line string) junieSessionSummary {
	return junieSessionSummary{
		projectDir: gjson.Get(line, "projectDir").Str,
		taskName:   gjson.Get(line, "taskName").Str,
		createdAt:  junieTimestamp(gjson.Get(line, "createdAt")),
		updatedAt:  junieTimestamp(gjson.Get(line, "updatedAt")),
	}
}

func junieTimestamp(value gjson.Result) time.Time {
	if value.Type != gjson.Number || value.Int() <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value.Int())
}

func junieMillisTimestamp(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func junieTimestampFromID(id string) time.Time {
	for i := 0; i+13 <= len(id); i++ {
		sub := id[i : i+13]
		if isDigit2(sub[0:2]) && isDigit2(sub[2:4]) && isDigit2(sub[4:6]) &&
			sub[6] == '-' &&
			isDigit2(sub[7:9]) && isDigit2(sub[9:11]) && isDigit2(sub[11:13]) {
			yy := (int(sub[0]-'0') * 10) + int(sub[1]-'0')
			mm := (int(sub[2]-'0') * 10) + int(sub[3]-'0')
			dd := (int(sub[4]-'0') * 10) + int(sub[5]-'0')
			hour := (int(sub[7]-'0') * 10) + int(sub[8]-'0')
			min := (int(sub[9]-'0') * 10) + int(sub[10]-'0')
			sec := (int(sub[11]-'0') * 10) + int(sub[12]-'0')
			if mm < 1 || mm > 12 || dd < 1 || dd > 31 ||
				hour > 23 || min > 59 || sec > 59 {
				continue
			}
			// time.Date normalizes out-of-range components; a candidate whose
			// round-trip differs from its input (Feb 30, Apr 31, ...) is not a
			// real timestamp embedded in the session ID.
			t := time.Date(2000+yy, time.Month(mm), dd, hour, min, sec, 0, time.UTC)
			if t.Year() == 2000+yy && int(t.Month()) == mm && t.Day() == dd &&
				t.Hour() == hour && t.Minute() == min && t.Second() == sec {
				return t
			}
		}
	}
	return time.Time{}
}

func isDigit2(s string) bool {
	return s[0] >= '0' && s[0] <= '9' && s[1] >= '0' && s[1] <= '9'
}
