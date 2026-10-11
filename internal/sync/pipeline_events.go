package sync

import (
	"time"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/pipevents"
)

// SetPipelineObserver installs the pipeline observer. It may be called
// while syncs are running; workers pick the change up at the next emit
// site. Passing nil disables instrumentation entirely.
func (e *Engine) SetPipelineObserver(o pipevents.Observer) {
	if o == nil {
		e.pipelineObs.Store(nil)
		return
	}
	e.pipelineObs.Store(&o)
}

// pipelineObserver returns the installed observer or nil. The atomic
// load keeps this cheap enough to run on every emit site.
func (e *Engine) pipelineObserver() pipevents.Observer {
	if p := e.pipelineObs.Load(); p != nil {
		return *p
	}
	return nil
}

// emitPipeline delivers one event to the installed observer. Callers
// that build a payload map must check pipelineObserver first so the nil
// path allocates nothing; emitPipeline re-checks for direct callers.
func (e *Engine) emitPipeline(
	stage pipevents.Stage,
	kind, path string,
	payload map[string]any,
) {
	ob := e.pipelineObserver()
	if ob == nil {
		return
	}
	ob.ObserveEvent(pipevents.Event{
		AtMS:    time.Now().UnixMilli(),
		Stage:   stage,
		Kind:    kind,
		Path:    path,
		Payload: payload,
	})
}

// pipelineObserverSource adapts the engine's observer slot to the lazy
// source contract parser providers accept: providers keep the function
// and resolve the current observer per event, so installing or swapping
// observers at runtime never requires rebuilding providers.
func (e *Engine) pipelineObserverSource() pipevents.Observer {
	return e.pipelineObserver()
}

// pipelineObserverFactory injects the engine's observer source into every
// provider it materializes. Wrapping the factories — rather than each
// NewProvider call site — covers every construction path uniformly,
// including ReconfigureSources rebuilds from baseProviderFactories and
// ad-hoc providers built for fingerprints or watch plans.
type pipelineObserverFactory struct {
	parser.ProviderFactory
	source parser.ObserverSource
}

func (f pipelineObserverFactory) NewProvider(
	cfg parser.ProviderConfig,
) parser.Provider {
	provider := f.ProviderFactory.NewProvider(cfg)
	if observed, ok := provider.(parser.PipelineObserverProvider); ok {
		observed.SetPipelineObserverSource(f.source)
	}
	return provider
}

// injectPipelineObserverSource wraps every factory so each materialized
// provider receives the engine's lazy observer source.
func injectPipelineObserverSource(
	factories []parser.ProviderFactory,
	source parser.ObserverSource,
) []parser.ProviderFactory {
	injected := make([]parser.ProviderFactory, len(factories))
	for i, factory := range factories {
		injected[i] = pipelineObserverFactory{
			ProviderFactory: factory,
			source:          source,
		}
	}
	return injected
}

// gateVerdict records one gate outcome for file. Every decision point in
// processProviderFile — cache lookups, freshness checks, checkpoint and
// fingerprint probes, rank and retention probes — emits exactly one
// verdict before returning or falling through to the next gate.
func (e *Engine) gateVerdict(
	file parser.DiscoveredFile,
	gate, decision, reason string,
	extra map[string]any,
) {
	if e.pipelineObserver() == nil {
		return
	}
	payload := map[string]any{
		"gate":     gate,
		"decision": decision,
		"reason":   reason,
	}
	for k, v := range extra {
		payload[k] = v
	}
	e.emitPipeline(pipevents.StageGates, pipevents.KindGateVerdict,
		file.Path, payload)
}

// emitParseMode records which parse path a source takes: incremental
// append, full parse, or staged full parse.
func (e *Engine) emitParseMode(
	file parser.DiscoveredFile,
	mode, reason string,
	extra map[string]any,
) {
	if e.pipelineObserver() == nil {
		return
	}
	payload := map[string]any{
		"mode":   mode,
		"reason": reason,
	}
	for k, v := range extra {
		payload[k] = v
	}
	e.emitPipeline(pipevents.StageParse, pipevents.KindParseModeSelected,
		file.Path, payload)
}

// emitDedupChoice records one resolution between two discovered files
// that identify the same session: the winner survives, the loser is
// dropped before parse.
func (e *Engine) emitDedupChoice(
	winner, loser parser.DiscoveredFile,
	uuid, reason string,
) {
	if e.pipelineObserver() == nil {
		return
	}
	payload := map[string]any{
		"uuid":   uuid,
		"winner": winner.Path,
		"loser":  loser.Path,
		"reason": reason,
	}
	e.emitPipeline(pipevents.StageResolve, pipevents.KindDedupWinner,
		winner.Path, payload)
	e.emitPipeline(pipevents.StageResolve, pipevents.KindDedupLoser,
		loser.Path, payload)
}

// codexCheckpointGateDecision folds the checkpoint enum into the gate
// decision vocabulary. codexCheckpointFallback and codexCheckpointMissing
// both mean "no usable checkpoint" for the gate: fallback covers sources
// without a stored row or whose proof the checkpoint cannot evaluate,
// missing covers rows whose checkpoint is absent or too old.
func codexCheckpointGateDecision(d codexCheckpointDecision) string {
	switch d {
	case codexCheckpointUnchanged:
		return "unchanged"
	case codexCheckpointAppend:
		return "append"
	case codexCheckpointInvalid:
		return "invalid"
	default:
		return "missing"
	}
}

func codexCheckpointDecisionReason(d codexCheckpointDecision) string {
	switch d {
	case codexCheckpointUnchanged:
		return "checkpoint proves transcript unchanged"
	case codexCheckpointAppend:
		return "checkpoint proves clean append"
	case codexCheckpointInvalid:
		return "checkpoint proof invalid"
	case codexCheckpointMissing:
		return "no stored checkpoint"
	default:
		return "no resumable checkpoint"
	}
}

// maxMessageOrdinal returns the highest ordinal in msgs. The batch write
// paths report it so event consumers can reconstruct ordering without
// re-reading the transcript.
func maxMessageOrdinal(msgs []db.Message) int {
	maxOrdinal := 0
	for _, m := range msgs {
		if m.Ordinal > maxOrdinal {
			maxOrdinal = m.Ordinal
		}
	}
	return maxOrdinal
}
