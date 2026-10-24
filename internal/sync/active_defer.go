package sync

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.kenn.io/agentsview/internal/parser"
)

// The active-writer defer gate mirrors the freshness half of the cass sync
// defer (indexer/mod.rs): a source whose mtime is seconds old AND that is
// still growing between two cheap stats is likely mid-write, so parsing it
// now mostly reparses the same append on the writer's next watcher event.
// Both lanes converge losslessly on torn tails, so the deferral is a churn
// and reparse-cost optimization for transient snapshots, not a loss guard.
// It is deliberately narrow: recency alone never defers (a quiet-young file
// parses immediately), only an observed write between the two admission
// samples does, and any source older than the window never defers at all —
// the age escape keeps a quiet-but-abandoned file from starving. The gate
// holds no persistent state: the writer's next event (or any later pass)
// re-enters admission, where an aged or stable source parses as usual.

const (
	// activeDeferDefaultWindow is the recency window: a source whose mtime
	// is younger than this may be deferred when it is still growing.
	activeDeferDefaultWindow = 5 * time.Second
	// activeDeferSampleDelay separates the two growth samples at admission.
	// Short enough to bound admission latency, long enough for an active
	// writer's next flush to land in between.
	activeDeferSampleDelay = 200 * time.Millisecond
)

// activeDeferEnvLookup is a test seam replacing the environment source for
// AGENTSVIEW_SYNC_ACTIVE_DEFER_SECONDS.
var activeDeferEnvLookup = os.Getenv

// activeDeferUnderGoTest is a test seam over testing.Testing, mirroring the
// telemetry reporter's go-test detection.
var activeDeferUnderGoTest = testing.Testing

// activeDeferWindow resolves the recency window from
// AGENTSVIEW_SYNC_ACTIVE_DEFER_SECONDS, in seconds. Unset or unusable values
// keep the default in production; "0", "false", and negative values disable
// the gate; a positive integer re-times the window. Under go test the unset
// default is off: the gate's sample wait inside admission deadlocks synctest
// bubbles and taxes every fixture that admits a freshly written source, so
// gate-specific tests opt in through the env seam instead.
func activeDeferWindow() time.Duration {
	raw := strings.TrimSpace(
		activeDeferEnvLookup("AGENTSVIEW_SYNC_ACTIVE_DEFER_SECONDS"),
	)
	if raw == "" {
		if activeDeferUnderGoTest() {
			return 0
		}
		return activeDeferDefaultWindow
	}
	if raw == "false" {
		return 0
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 0 {
		return activeDeferDefaultWindow
	}
	return time.Duration(secs) * time.Second
}

// activeDeferClock and activeDeferWait inject the gate's clock and
// inter-sample wait; nil fields use time.Now and time.Sleep.
func (e *Engine) activeDeferClock() time.Time {
	if e.activeDeferNow != nil {
		return e.activeDeferNow()
	}
	return time.Now()
}

func (e *Engine) activeDeferWait() {
	if e.activeDeferSleep != nil {
		e.activeDeferSleep(activeDeferSampleDelay)
		return
	}
	time.Sleep(activeDeferSampleDelay)
}

// applyActiveWriteDefer is the parse-admission defer for the freshest
// growing sources, placed beside the per-source byte budget so every lane
// that reaches a parse decision sees the same deferral. A source inside the
// recency window that grows (size or mtime) between two admission stats is
// skipped for this pass without a failure ledger — it is healthy, just
// mid-write — and the anomaly counter records the deferral. The returned
// result is a proof-withheld skip: no skip-cache entry or freshness
// watermark is written, and the pass is not authoritative for the source,
// so the next event or pass re-admits it.
func (e *Engine) applyActiveWriteDefer(
	file parser.DiscoveredFile, source parser.SourceRef,
) (processResult, bool) {
	window := activeDeferWindow()
	if window <= 0 {
		return processResult{}, false
	}
	// An explicit refresh is a caller-requested parse, not background
	// churn: never defer it behind a writer that may never finish.
	if e.forceParseBypassesCache(file) || file.ForceFullParse {
		return processResult{}, false
	}
	// The byte budget's stat discipline: the source file itself, or the
	// container database behind a virtual source. An unstatable or
	// directory-backed source falls through to the ordinary gates. Tests
	// hand-build engines without a stat func; fall back like the other
	// stat call sites do.
	path := validatedProviderSourceStatPath(providerDiscoveredPath(source))
	stat := e.stat
	if stat == nil {
		stat = os.Stat
	}
	first, err := stat(path)
	if err != nil || first.IsDir() {
		return processResult{}, false
	}
	// Age escape: outside the window the source parses as usual, and no
	// sample delay is ever paid for it.
	if age := e.activeDeferClock().Sub(first.ModTime()); age >= window {
		return processResult{}, false
	}
	e.activeDeferWait()
	second, err := stat(path)
	if err != nil || second.IsDir() {
		return processResult{}, false
	}
	// Defer only on observed growth: a size increase, or a same-size write
	// that moved the mtime (an in-place rewrite). A young-but-stable source
	// proceeds — recency alone is not evidence of an active writer.
	if second.Size() <= first.Size() &&
		!second.ModTime().After(first.ModTime()) {
		return processResult{}, false
	}
	log.Printf(
		"sync: deferring %s: wrote %d→%d bytes inside the %s active-writer window",
		file.Path, first.Size(), second.Size(), window,
	)
	e.anomalies.recordActiveDefer(
		string(file.Agent), providerAgentSkipCacheKey(file.Path, file.Agent),
	)
	e.gateVerdict(file, "active_defer", "skip",
		fmt.Sprintf("still growing inside %s window (%d→%d bytes)",
			window, first.Size(), second.Size()),
		map[string]any{
			"size_first":  first.Size(),
			"size_second": second.Size(),
		})
	return processResult{
		skip:                  true,
		suppressPresenceSweep: true,
	}, true
}
