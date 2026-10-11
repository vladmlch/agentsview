package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/tidwall/gjson"

	"go.kenn.io/agentsview/internal/pipevents"
	"go.kenn.io/agentsview/internal/stringutil"
)

// ObserverSource resolves the active observer lazily so an engine can
// toggle instrumentation at runtime without rebuilding providers.
type ObserverSource func() pipevents.Observer

// PipelineObserverProvider is implemented by providers that can report
// fine-grained parse events.
type PipelineObserverProvider interface {
	SetPipelineObserverSource(ObserverSource)
}

// PipelineObserverSourceProvider is implemented by parse sinks that carry
// their own observer source. A parser streaming into such a sink adopts
// the carried source when the provider itself has none installed, so the
// staged parse path lights up the same event stream as the collecting one.
type PipelineObserverSourceProvider interface {
	PipelineObserverSource() ObserverSource
}

// codexParseObserver couples a lazily resolved observer with the
// transcript path emitted events are attributed to. The zero value is
// valid: a nil source resolves to a nil observer and every emit site
// returns before allocating any payload.
type codexParseObserver struct {
	source ObserverSource
	path   string
}

// observer resolves the installed observer, or nil when instrumentation
// is disabled. Emit sites resolve before building payload maps so the
// uninstrumented path allocates nothing.
func (o codexParseObserver) observer() pipevents.Observer {
	if o.source == nil {
		return nil
	}
	return o.source()
}

// emit delivers one event. It re-checks the resolved observer so helpers
// that already gated on it can pass a prebuilt payload safely even if the
// observer was toggled between the gate and the delivery.
func (o codexParseObserver) emit(kind string, payload map[string]any) {
	ob := o.observer()
	if ob == nil {
		return
	}
	ob.ObserveEvent(pipevents.Event{
		AtMS:    time.Now().UnixMilli(),
		Stage:   pipevents.StageParse,
		Kind:    kind,
		Path:    o.path,
		Payload: payload,
	})
}

func (o codexParseObserver) emitParseStarted(
	mode string, offset, size int64, seeded bool,
) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindParseStarted, map[string]any{
		"mode":   mode,
		"offset": offset,
		"size":   size,
		"seeded": seeded,
	})
}

func (o codexParseObserver) emitParseDone(
	messages int, consumedBytes int64, safe, checkpoint bool,
) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindParseDone, map[string]any{
		"messages":       messages,
		"consumed_bytes": consumedBytes,
		"safe":           safe,
		"checkpoint":     checkpoint,
	})
}

// codexLineRawLimit bounds the raw transcript slice a line_classified
// event carries; longer lines report truncated: true.
const codexLineRawLimit = 4 << 10

// emitLineClassified reports one physical line the parse loop classified:
// its 1-based ordinal within this parse, the absolute byte offset just
// past the line (-1 when the supplying reader cannot track offsets), the
// classified envelope type ("malformed" for lines that are not valid
// JSON), and a bounded raw excerpt.
func (o codexParseObserver) emitLineClassified(
	lineNo int, endOffset int64, line string,
) {
	if o.observer() == nil {
		return
	}
	lineType := gjson.Get(line, "type").Str
	if !gjson.Valid(line) {
		lineType = "malformed"
	}
	raw := stringutil.SafeTruncate(line, codexLineRawLimit)
	payload := map[string]any{
		"line_no": lineNo,
		"offset":  endOffset,
		"type":    lineType,
		"raw":     raw,
	}
	if len(raw) < len(line) {
		payload["truncated"] = true
	}
	o.emit(pipevents.KindLineClassified, payload)
}

// codexMessageDigest is the compact content fingerprint carried by
// message_emitted events: enough bits to correlate a message with a
// stored row without shipping its content.
func codexMessageDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// codexMessageKind derives a coarse event kind from the normalized
// message shape: an explicit source subtype wins, a message carrying
// tool calls reports "tool_call", everything else is a plain "message".
func codexMessageKind(m ParsedMessage) string {
	if m.SourceSubtype != "" {
		return m.SourceSubtype
	}
	if len(m.ToolCalls) > 0 {
		return "tool_call"
	}
	return "message"
}

func (o codexParseObserver) emitMessageEmitted(m ParsedMessage) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindMessageEmitted, map[string]any{
		"ordinal": m.Ordinal,
		"kind":    codexMessageKind(m),
		"role":    string(m.Role),
		"digest":  codexMessageDigest(m.Content),
	})
}

func (o codexParseObserver) emitCursorUpdated(
	offset int64, cursor *codexCursorState, nextOrdinal int,
) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindCursorUpdated, map[string]any{
		"offset":           offset,
		"pending_calls":    int(cursor.pendingCallCount),
		"first_user_seen":  cursor.firstUserSeen,
		"fork_gate_active": cursor.forkGate.active,
		"next_ordinal":     nextOrdinal,
	})
}

func (o codexParseObserver) emitIncrementalFallback(reason string, lineNo int) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindIncrementalFallback, map[string]any{
		"reason":  reason,
		"line_no": lineNo,
	})
}

// emitParseFailed closes the event stream on a hard-error exit so an
// observer never sees a parse_started left unterminated. It reports the
// same shape as emitParseDone plus the failure.
func (o codexParseObserver) emitParseFailed(err error) {
	if o.observer() == nil {
		return
	}
	o.emit(pipevents.KindParseDone, map[string]any{
		"messages":       0,
		"consumed_bytes": int64(0),
		"safe":           false,
		"checkpoint":     false,
		"error":          err.Error(),
	})
}
