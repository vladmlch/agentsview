package sync

import (
	"context"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/shirou/gopsutil/v4/load"
)

// The load governor adapts the parse worker pool to sampled system load,
// mirroring the shrink-under-pressure half of the cass sync governor
// (responsiveness.rs): each sync pass builds a fresh worker pool, so the
// pool-creation seam in Engine.workerCount is the natural re-sample point.
// Scaling is shrink-only — an idle machine never earns more workers than
// the CPU-count baseline — and any sampling failure (unsupported platform,
// timeout) falls back to the ungoverned baseline.

const (
	// governorHighLoadPerCPU is the load-per-core ratio above which the
	// pool is cut to half the baseline.
	governorHighLoadPerCPU = 2.0
	// governorMediumLoadPerCPU is the load-per-core ratio above which the
	// pool is cut to three quarters of the baseline.
	governorMediumLoadPerCPU = 1.5
	// loadSampleTimeout bounds a single load-average sample. Reading
	// /proc/loadavg or sysctl is fast; the timeout only guards against a
	// wedged platform backend stalling pool creation.
	loadSampleTimeout = 500 * time.Millisecond
)

// governorEnvLookup is a test seam replacing the environment source for
// AGENTSVIEW_SYNC_GOVERNOR.
var governorEnvLookup = os.Getenv

// governorEnabled reports whether load-adaptive worker scaling is active.
// AGENTSVIEW_SYNC_GOVERNOR=0|false disables it; the default is on.
func governorEnabled() bool {
	switch v := governorEnvLookup("AGENTSVIEW_SYNC_GOVERNOR"); v {
	case "0", "false":
		return false
	default:
		return true
	}
}

// governedWorkerCount applies shrink-only load scaling to base. ratio is
// loadAvg divided across cpuCount cores (a non-positive cpuCount is treated
// as one core to avoid division by zero). It never returns more than base
// and never less than one worker.
func governedWorkerCount(base int, loadAvg float64, cpuCount int) int {
	if base <= 0 {
		return base
	}
	ratio := loadAvg / float64(max(cpuCount, 1))
	switch {
	case ratio > governorHighLoadPerCPU:
		return max(base/2, 1)
	case ratio > governorMediumLoadPerCPU:
		return max(base*3/4, 1)
	default:
		return base
	}
}

// loadAvgSampler returns a sampled 1-minute load average. The engine field
// of the same name is the injection seam; production uses gopsutilLoadAvg.
type loadAvgSampler func(ctx context.Context) (float64, error)

// gopsutilLoadAvg samples the system load average through gopsutil.
// Platforms without a loadavg backend (e.g. the gopsutil fallback) return
// an error, which the governor treats as "do not adapt".
func gopsutilLoadAvg(ctx context.Context) (float64, error) {
	sampleCtx, cancel := context.WithTimeout(ctx, loadSampleTimeout)
	defer cancel()
	avg, err := load.AvgWithContext(sampleCtx)
	if err != nil {
		return 0, err
	}
	return avg.Load1, nil
}

// applyLoadGovernor adapts base for the current system load. Sampling is
// skipped when the governor is disabled via AGENTSVIEW_SYNC_GOVERNOR; a
// sampler error keeps base unchanged. A shrink is logged once per pool
// creation so operators can correlate slower passes with load pressure.
func (e *Engine) applyLoadGovernor(ctx context.Context, base int) int {
	if !governorEnabled() {
		return base
	}
	sample := e.loadAvgSampler
	if sample == nil {
		sample = gopsutilLoadAvg
	}
	loadAvg, err := sample(ctx)
	if err != nil {
		return base
	}
	cpuCount := runtime.NumCPU()
	workers := governedWorkerCount(base, loadAvg, cpuCount)
	if workers < base {
		log.Printf(
			"load governor: ratio %.2f → workers %d → %d",
			loadAvg/float64(max(cpuCount, 1)), base, workers,
		)
	}
	return workers
}
