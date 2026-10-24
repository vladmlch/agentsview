package sync

import (
	"fmt"
	"log"

	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sync/failurecache"
)

// budgetedSourceSize is the byte size the per-source budget admits against,
// plus the stat identity that keys a budget skip in the source-failure cache.
type budgetedSourceSize struct {
	size    int64
	mtimeNS int64
}

// sourceBudgetSize resolves the admission size for one source: the source
// file itself, or — for a virtual source inside a shared SQLite container
// (Devin, OpenCode-format storage) — the container database file that backs
// it. Directory-backed sources have no single admission size and are not
// budget-limited. ok is false when the size cannot be determined cheaply;
// admission then falls through so the fingerprint path reports the real
// reason.
func (e *Engine) sourceBudgetSize(source parser.SourceRef) (budgetedSourceSize, bool) {
	path := validatedProviderSourceStatPath(providerDiscoveredPath(source))
	info, err := e.stat(path)
	if err != nil || info.IsDir() {
		return budgetedSourceSize{}, false
	}
	return budgetedSourceSize{
		size:    info.Size(),
		mtimeNS: info.ModTime().UnixNano(),
	}, true
}

// applySourceByteBudget is the parse-admission gate for the per-source byte
// ceiling: a source whose admission size exceeds sync_max_source_bytes is
// skipped with a recorded source failure, so the source_failures lane retries
// it when its stat identity changes. A resync resets the failure cache, which
// re-admits over-budget sources after the ceiling is raised. The returned
// result is a proof-withheld skip: no skip-cache entry or freshness watermark
// is written for an over-budget source.
func (e *Engine) applySourceByteBudget(
	file parser.DiscoveredFile, source parser.SourceRef,
) (processResult, bool) {
	if e.maxSourceBytes <= 0 {
		return processResult{}, false
	}
	size, ok := e.sourceBudgetSize(source)
	if !ok || size.size <= e.maxSourceBytes {
		return processResult{}, false
	}
	log.Printf(
		"sync: skipping %s: %d bytes exceeds sync_max_source_bytes (%d)",
		file.Path, size.size, e.maxSourceBytes,
	)
	e.failures.Record(
		providerAgentSkipCacheKey(file.Path, file.Agent),
		failurecache.Identity{
			MTimeNS: size.mtimeNS,
			Size:    size.size,
		},
	)
	e.gateVerdict(file, "source_budget", "skip",
		fmt.Sprintf("%d bytes over %d ceiling", size.size, e.maxSourceBytes), nil)
	return processResult{
		skip:                  true,
		cachedFailure:         true,
		suppressPresenceSweep: true,
	}, true
}
