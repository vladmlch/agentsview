package parser

import (
	"log"
	"slices"
	gosync "sync"
)

// CodexLayoutChange is one observed layout drift for a single Codex-format
// sessions root between two discovery passes in the same process. It is the
// validation-only av-19 receipt signal: while the layout-independent
// agent:UUID source key and the live-over-archived dedup hold, a flat↔dated
// rename cannot fork history, so the receipt only surfaces drift early — it
// never migrates anything.
type CodexLayoutChange struct {
	Agent AgentType `json:"agent"`
	Root  string    `json:"root"`
	// From and To name the observed layout set before and after the drift
	// ("flat", "dated", or "flat+dated").
	From string `json:"from"`
	To   string `json:"to"`
}

// CodexLayoutChangeSink receives per-root Codex layout drift records from
// the validation-only layout receipt. The sync engine injects its per-run
// anomaly accumulator at provider construction, mirroring the
// pipeline-observer injection, so drift lands in the run's anomaly summary.
type CodexLayoutChangeSink interface {
	RecordCodexLayoutChange(change CodexLayoutChange)
}

// CodexLayoutChangeSinkProvider is implemented by Codex-format providers so
// the sync engine can inject a per-run layout-drift sink.
type CodexLayoutChangeSinkProvider interface {
	SetCodexLayoutChangeSink(sink CodexLayoutChangeSink)
}

// codexLayoutFlags is the set of Codex session layouts observed under one
// root in a single discovery pass. A root may legitimately hold both layouts
// at once (live dated transcripts plus flat archived copies).
type codexLayoutFlags uint8

const (
	codexFlatObserved  codexLayoutFlags = 1 << iota
	codexDatedObserved codexLayoutFlags = 1 << iota
)

func (f codexLayoutFlags) String() string {
	switch f {
	case codexFlatObserved:
		return "flat"
	case codexDatedObserved:
		return "dated"
	case codexFlatObserved | codexDatedObserved:
		return "flat+dated"
	default:
		return "none"
	}
}

// maxDualLayoutTrackedUUIDs bounds the per-pass uuid→layout map used for
// same-uuid-in-both-layouts detection, so a huge archive cannot grow a
// streaming walk's memory without bound. Past the cap the walk stops
// tracking (dual detection becomes partial for that pass) instead of
// growing.
const maxDualLayoutTrackedUUIDs = 4096

// maxDualLayoutWarnings bounds the per-process dedup set of dual-layout
// warnings, so a permanently mixed archive warns once per root+uuid and
// then stops tracking instead of growing across passes.
const maxDualLayoutWarnings = 256

// codexLayoutObservation accumulates one root's layout signals across a
// single discovery pass. The registry compares the committed union between
// passes; within a pass the filesystem is not re-read, so mid-pass
// observations of the same root are idempotent.
type codexLayoutObservation struct {
	flags codexLayoutFlags
	// uuidLayouts records which layouts each observed session UUID
	// appeared in, for dual-layout detection. Bounded by
	// maxDualLayoutTrackedUUIDs.
	uuidLayouts map[string]codexLayoutFlags
}

func (o *codexLayoutObservation) note(layout CodexLayout, uuid string) {
	var flag codexLayoutFlags
	switch layout {
	case CodexLayoutArchivedFlat:
		flag = codexFlatObserved
	case CodexLayoutDated:
		flag = codexDatedObserved
	default:
		return
	}
	o.flags |= flag
	if uuid == "" || len(o.uuidLayouts) >= maxDualLayoutTrackedUUIDs {
		return
	}
	if o.uuidLayouts == nil {
		o.uuidLayouts = make(map[string]codexLayoutFlags)
	}
	o.uuidLayouts[uuid] |= flag
}

// dualLayoutUUIDs returns the session UUIDs observed under both layouts in
// this pass, sorted for deterministic logging.
func (o *codexLayoutObservation) dualLayoutUUIDs() []string {
	var duals []string
	for uuid, flags := range o.uuidLayouts {
		if flags == codexFlatObserved|codexDatedObserved {
			duals = append(duals, uuid)
		}
	}
	if duals == nil {
		return nil
	}
	slices.Sort(duals)
	return duals
}

// codexLayoutReceipts is the in-memory, validation-only layout receipt
// registry for one Codex-format provider factory. It remembers each root's
// last committed layout observation so a later pass can detect drift, and
// warns (once per process) when a session UUID resolves through both
// layouts under one root — the shape a history-forking rename would take.
// Nothing is persisted: a restart re-receipts every root as a first
// observation, which is acceptable for a validation hedge (audit av-19).
type codexLayoutReceipts struct {
	mu         gosync.Mutex
	agent      AgentType
	observed   map[string]codexLayoutFlags
	dualWarned map[string]bool
}

func newCodexLayoutReceipts(agent AgentType) *codexLayoutReceipts {
	return &codexLayoutReceipts{
		agent:      agent,
		observed:   make(map[string]codexLayoutFlags),
		dualWarned: make(map[string]bool),
	}
}

// observe commits one pass's observation for root. A pass that saw no
// session files commits nothing: an empty root says nothing about layout,
// and committing it would turn the first real session into a spurious
// drift warning. Returns the drift records (at most one) for the caller to
// deliver to its injected sink; log lines are emitted here.
func (r *codexLayoutReceipts) observe(
	root string, obs codexLayoutObservation,
) []CodexLayoutChange {
	if obs.flags == 0 {
		return nil
	}
	r.mu.Lock()
	prev, seenBefore := r.observed[root]
	r.observed[root] = obs.flags
	var duals []string
	for _, uuid := range obs.dualLayoutUUIDs() {
		key := root + "\x00" + uuid
		if r.dualWarned[key] {
			continue
		}
		if len(r.dualWarned) >= maxDualLayoutWarnings {
			break
		}
		r.dualWarned[key] = true
		duals = append(duals, uuid)
	}
	r.mu.Unlock()

	if !seenBefore {
		log.Printf("codex layout receipt: %s sessions root %q uses %s (first observation)",
			r.agent, root, obs.flags)
	}
	for _, uuid := range duals {
		log.Printf("WARNING: codex dual-layout uuid: session %s resolves through both flat and dated layouts under %s root %q; the live-over-archived dedup guards the stored copy",
			uuid, r.agent, root)
	}
	if seenBefore && prev != obs.flags {
		log.Printf("WARNING: codex layout change: %s sessions root %q changed %s -> %s; external-id scope may have shifted across layouts; same-UUID copies stay deduped by the live-over-archived rule",
			r.agent, root, prev, obs.flags)
		return []CodexLayoutChange{{
			Agent: r.agent,
			Root:  root,
			From:  prev.String(),
			To:    obs.flags.String(),
		}}
	}
	return nil
}
