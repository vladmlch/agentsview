// Package pipevents defines the event vocabulary shared by the
// ingest pipeline's instrumentation points and the tooling that
// records, replays, and visualizes those events. It is a leaf
// package: it must not import other internal packages, since
// sync, parser, pipeline, and server all import it.
package pipevents

// Stage identifies the pipeline stage that produced an Event.
type Stage string

const (
	StageWatch    Stage = "watch"
	StageResolve  Stage = "resolve"
	StageGates    Stage = "gates"
	StageParse    Stage = "parse"
	StageBatch    Stage = "batch"
	StageWrite    Stage = "write"
	StageFinalize Stage = "finalize"
	StageEmbed    Stage = "embed"
)

// Kind values (string constants; exhaustive list).
const (
	KindFSEvent             = "fs_event"
	KindBatchFormed         = "batch_formed"
	KindBatchDispatched     = "batch_dispatched"
	KindPathClassified      = "path_classified"
	KindSourceResolved      = "source_resolved"
	KindDedupWinner         = "dedup_winner"
	KindDedupLoser          = "dedup_loser"
	KindGateVerdict         = "gate_verdict"
	KindParseStarted        = "parse_started"
	KindParseDone           = "parse_done"
	KindLineClassified      = "line_classified"
	KindMessageEmitted      = "message_emitted"
	KindCursorUpdated       = "cursor_updated"
	KindIncrementalFallback = "incremental_fallback"
	KindStaging             = "staging"
	KindResultAdmitted      = "result_admitted"
	KindPendingAccumulated  = "pending_accumulated"
	KindFlushTriggered      = "flush_triggered"
	KindTxStarted           = "tx_started"
	KindSessionWritten      = "session_written"
	KindMessagesWritten     = "messages_written"
	KindFTSUpdated          = "fts_updated"
	KindCheckpointWritten   = "checkpoint_written"
	KindTxCommitted         = "tx_committed"
	KindLinksResolved       = "links_resolved"
	KindSSEEmitted          = "sse_emitted"
	KindSyncPassStarted     = "sync_pass_started"
	KindSyncPassDone        = "sync_pass_done"
	KindScenarioStep        = "scenario_step"
	KindEmbedScheduled      = "embed_scheduled"
	KindEmbedStarted        = "embed_started"
	KindEmbedDone           = "embed_done"
	KindEmbedSkipped        = "embed_skipped"
	KindRunStarted          = "run_started"
	KindRunFinished         = "run_finished"
)

// Event is one observed step of the ingest pipeline. Producers
// leave Seq and RunID zero; the recording hub stamps them.
type Event struct {
	Seq     uint64         `json:"seq"`
	AtMS    int64          `json:"at_ms"` // unix milliseconds
	RunID   string         `json:"run_id,omitempty"`
	Stage   Stage          `json:"stage"`
	Kind    string         `json:"kind"`
	Path    string         `json:"path,omitempty"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Observer receives pipeline events. Implementations must be
// thread-safe and must not block; called from sync workers.
type Observer interface{ ObserveEvent(Event) }

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// ObserveEvent calls f(e).
func (f ObserverFunc) ObserveEvent(e Event) { f(e) }
