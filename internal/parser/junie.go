package parser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"go.kenn.io/agentsview/internal/money"
)

type junieSessionSummary struct {
	projectDir string
	taskName   string
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
	id        string
	name      string
	kind      string
	stepID    string
	blocks    map[string]*junieBlock
	blockOrd  []string
	request   string
	requestTS time.Time
	usage     []ParsedUsageEvent
	usageSeen map[string]struct{}
	cwd       string
	taskName  string
	firstTS   time.Time
	lastTS    time.Time
	notes     []ParsedMessage
	cancelled bool
	failed    bool
	state     string
}

func newJunieAgentStream(id string) *junieAgentStream {
	return &junieAgentStream{
		id:        id,
		blocks:    make(map[string]*junieBlock),
		usageSeen: make(map[string]struct{}),
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
) (*ParsedSession, []ParsedMessage, bool, error) {
	f, err := openJunieEventStream(path, openRoot)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, false, fmt.Errorf("stat %s: %w", path, err)
	}

	state := junieParserState{
		userMessages:    make(map[string]int),
		streams:         map[string]*junieAgentStream{"main": newJunieAgentStream("main")},
		sourceSessionID: filepath.Base(filepath.Dir(path)),
	}
	lr := newLineReaderContext(ctx, f, maxLineSize)
	defer releaseLineReader(lr)
	for lineNumber := 1; ; lineNumber++ {
		line, ok := lr.next()
		if !ok {
			break
		}
		if err := contextErrEvery(ctx, lineNumber); err != nil {
			return nil, nil, false, err
		}
		if !gjson.Valid(line) {
			state.malformedLines++
			state.lastLineBad = true
			continue
		}
		state.lastLineBad = false
		if err := state.consumeEvent(gjson.Parse(line), lineNumber); err != nil {
			return nil, nil, false, fmt.Errorf("parsing Junie session %s line %d: %w", path, lineNumber, err)
		}
	}
	if err := lr.Err(); err != nil {
		return nil, nil, false, fmt.Errorf("reading Junie session %s: %w", path, err)
	}
	if lr.skippedOversized {
		return nil, nil, false, fmt.Errorf("reading Junie session %s: record exceeds %d bytes", path, maxLineSize)
	}
	return state.session(
		ctx, path, machine, filepath.Base(filepath.Dir(path)),
		info.Size(), info.ModTime().UnixNano(), summary, summaryPresent,
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
		s.consumeUserPrompt(event, timestamp)
	case "UserResponseEvent":
		s.appendMessage(RoleUser, event.Get("prompt").Str, timestamp)
	case "UserAsyncResponseEvent":
		s.consumeAsyncResponse(event, timestamp)
	case "UserMessagesCommittedToHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), true)
	case "UserMessagesDroppedFromHistory", "UserMessagesFailedInHistory":
		s.setUserMessagesActive(event.Get("userMessageIds"), false)
	case "SessionTitleSetEvent":
		s.sessionName = event.Get("name").Str
		s.sessionNameFound = true
	case "SystemMessageEvent":
		s.consumeSystemMessage(event, timestamp)
	case "AgentTaskFailedEvent":
		s.appendMessage(RoleSystem, "Agent task failed", timestamp)
	case "SessionA2uxEvent":
		return s.consumeA2UXEvent(event, timestamp, lineNumber)
	}
	return nil
}

func (s *junieParserState) consumeUserPrompt(event gjson.Result, timestamp time.Time) {
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
		return
	}
	index := s.appendMessage(RoleUser, content, timestamp)
	s.entries[index].active = active
	if requestID != "" {
		s.userMessages[requestID] = index
	}
}

func (s *junieParserState) consumeAsyncResponse(event gjson.Result, timestamp time.Time) {
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
	s.appendMessage(RoleUser, strings.Join(responses, "\n\n"), timestamp)
}

func (s *junieParserState) consumeSystemMessage(event gjson.Result, timestamp time.Time) {
	text := strings.TrimSpace(event.Get("text").Str)
	details := strings.TrimSpace(event.Get("details").Str)
	if details != "" && details != text {
		text = strings.TrimSpace(text + "\n\n" + details)
	}
	s.appendMessage(RoleSystem, text, timestamp)
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
			agentID = firstNonEmptyJSONLString(
				ae.Get("agent.name").Str,
				strings.TrimSuffix(strings.TrimSuffix(
					ae.Get("stepId").Str, "-request"), "-spawned"),
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
			if strings.HasSuffix(rawStepID, "-request") ||
				strings.HasSuffix(rawStepID, "-spawned") {
				stream.stepID = strings.TrimSuffix(strings.TrimSuffix(
					rawStepID, "-request"), "-spawned")
			} else if stream.stepID == "" {
				stream.stepID = rawStepID
			}
		}
	}
	taskID := event.Get("taskId").Str
	eventState := event.Get("event.state").Str
	if eventState == "CANCELLED" || eventState == "CANCELED" {
		if agentID == "main" {
			s.lastTaskState = "CANCELLED"
		} else {
			stream.cancelled = true
			stream.state = "CANCELLED"
		}
	} else if eventState == "FAILED" || eventState == "ERROR" {
		if agentID == "main" {
			if s.lastTaskState == "" || s.lastTaskState == "COMPLETED" {
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
		if agentID == "main" {
			junieUpsertBlock(stream, ae, kind, taskID, eventState, lineNumber, timestamp)
		}
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
		stepID = kind + "#" + ae.Raw[:minInt(len(ae.Raw), 64)]
	}
	status := firstNonEmptyJSONLString(ae.Get("status").Str, eventState)
	cancelled := ae.Get("cancelled").Bool() ||
		status == "CANCELLED" ||
		status == "CANCELED" ||
		eventState == "CANCELLED" ||
		eventState == "CANCELED" ||
		(stream != nil && stream.cancelled)

	text := firstNonEmptyJSONLString(ae.Get("text").Str, ae.Get("result").Str)
	if kind == "ResultBlockUpdatedEvent" {
		text = strings.TrimPrefix(text, "<!-- ANSWER -->")
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
		title:     ae.Get("title").Str,
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
		}
		stream.stepID = strings.TrimSuffix(stepID, "-request")
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
	stream *junieAgentStream, extra []ParsedMessage,
) []ParsedMessage {
	var taskIDs []string
	byTask := make(map[string][]*junieBlock)
	for _, stepID := range stream.blockOrd {
		b := stream.blocks[stepID]
		if b == nil {
			continue
		}
		if _, seen := byTask[b.taskID]; !seen {
			taskIDs = append(taskIDs, b.taskID)
			byTask[b.taskID] = []*junieBlock{b}
			continue
		}
		byTask[b.taskID] = append(byTask[b.taskID], b)
	}

	var merged []ParsedMessage
	merged = append(merged, extra...)
	for _, taskID := range taskIDs {
		if msg, ok := junieAssistantMessage(byTask[taskID], stream); ok {
			merged = append(merged, msg)
		}
	}
	merged = append(merged, stream.notes...)

	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Timestamp.Before(merged[j].Timestamp)
	})
	for i := range merged {
		merged[i].Ordinal = i
	}
	return merged
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
			// Tool calls in Task 2
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

func (s *junieParserState) appendMessage(
	role RoleType, content string, timestamp time.Time,
) int {
	s.entries = append(s.entries, junieTranscriptMessage{
		Role:          role,
		Content:       content,
		Timestamp:     timestamp,
		IsSystem:      role == RoleSystem,
		ContentLength: len(content),
		active:        strings.TrimSpace(content) != "",
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

func (s *junieParserState) session(
	ctx context.Context, path, machine, sourceSessionID string,
	fileSize, mtime int64,
	summary junieSessionSummary, summaryPresent bool,
) (*ParsedSession, []ParsedMessage, bool, error) {
	main := s.streams["main"]
	if main == nil {
		main = newJunieAgentStream("main")
	}
	var extra []ParsedMessage
	userCount := 0
	firstMessage := ""
	for _, entry := range s.entries {
		if !entry.active || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		extra = append(extra, entry.ParsedMessage)
		if entry.Role == RoleUser && !entry.IsSystem {
			userCount++
			if firstMessage == "" {
				firstMessage = truncate(strings.ReplaceAll(entry.Content, "\n", " "), 300)
			}
		}
	}
	messages := junieAssembleMessages(main, extra)

	hasActivity := len(main.blocks) > 0 || len(main.usage) > 0
	if len(messages) == 0 && len(s.entries) == 0 && !hasActivity && !s.sessionNameFound {
		return nil, nil, false, nil
	}
	if userCount == 0 && !hasActivity && !s.sessionNameFound && !summaryPresent {
		return nil, nil, true, nil
	}

	if !summary.createdAt.IsZero() &&
		(s.startedAt.IsZero() || summary.createdAt.Before(s.startedAt)) {
		s.startedAt = summary.createdAt
	}
	if summary.updatedAt.After(s.endedAt) {
		s.endedAt = summary.updatedAt
	}
	if !s.sessionNameFound && summaryPresent {
		s.sessionName = summary.taskName
	}
	if firstMessage == "" && strings.TrimSpace(s.sessionName) != "" {
		firstMessage = s.sessionName
	}

	project := ExtractProjectFromCwdWithBranchContext(
		WithoutFilesystemProjectDiscovery(ctx), summary.projectDir, "",
	)
	if project == "" {
		project = "junie"
	}
	projectDir := strings.TrimSpace(summary.projectDir)
	cwd := projectDir
	if cwd == "" {
		cwd = main.cwd
	}
	if filepath.IsAbs(cwd) {
		cwd = filepath.Clean(cwd)
	} else if !strings.HasPrefix(cwd, "/") && !looksLikeWindowsPath(cwd) {
		cwd = ""
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
		SessionNamePresent: s.sessionNameFound || summaryPresent,
		StartedAt:          s.startedAt,
		EndedAt:            s.endedAt,
		MessageCount:       len(messages),
		UserMessageCount:   userCount,
		MalformedLines:     s.malformedLines,
		File: FileInfo{
			Path:  path,
			Size:  fileSize,
			Mtime: mtime,
		},
	}
	applyUsageEventTokenTotals(session, usageEvents)
	session.UsageEvents = usageEvents
	return session, messages, false, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
