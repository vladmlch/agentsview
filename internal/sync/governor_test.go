package sync

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGovernedWorkerCount(t *testing.T) {
	tests := []struct {
		name     string
		base     int
		loadAvg  float64
		cpuCount int
		want     int
	}{
		{
			name:     "idle load keeps baseline",
			base:     8,
			loadAvg:  0,
			cpuCount: 4,
			want:     8,
		},
		{
			name:     "ratio at medium boundary keeps baseline",
			base:     8,
			loadAvg:  6, // 6/4 == 1.5, not above
			cpuCount: 4,
			want:     8,
		},
		{
			name:     "ratio just above medium shrinks to three quarters",
			base:     8,
			loadAvg:  6.1, // 6.1/4 > 1.5
			cpuCount: 4,
			want:     6,
		},
		{
			name:     "ratio at high boundary still three quarters",
			base:     8,
			loadAvg:  8, // 8/4 == 2.0, not above
			cpuCount: 4,
			want:     6,
		},
		{
			name:     "ratio above high halves",
			base:     8,
			loadAvg:  8.1, // 8.1/4 > 2.0
			cpuCount: 4,
			want:     4,
		},
		{
			name:     "three quarters floors at one worker",
			base:     2,
			loadAvg:  2, // 2/1 > 1.5
			cpuCount: 1,
			want:     1, // 2*3/4 == 1
		},
		{
			name:     "half floors at one worker",
			base:     1,
			loadAvg:  10,
			cpuCount: 1,
			want:     1,
		},
		{
			name:     "negative load keeps baseline",
			base:     4,
			loadAvg:  -1,
			cpuCount: 4,
			want:     4,
		},
		{
			name:     "zero cpu count treated as one core",
			base:     4,
			loadAvg:  3, // 3/1 > 2.0
			cpuCount: 0,
			want:     2,
		},
		{
			name:     "negative cpu count treated as one core",
			base:     4,
			loadAvg:  2, // 2/1 > 1.5
			cpuCount: -2,
			want:     3,
		},
		{
			name:     "non-positive base passes through",
			base:     0,
			loadAvg:  100,
			cpuCount: 4,
			want:     0,
		},
		{
			name:     "never grows beyond base regardless of cores",
			base:     3,
			loadAvg:  0.5,
			cpuCount: 64,
			want:     3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, governedWorkerCount(tt.base, tt.loadAvg, tt.cpuCount))
		})
	}
}

// baselineWorkerCount mirrors the ungoverned production baseline so wiring
// tests stay deterministic across host CPU counts.
func baselineWorkerCount() int {
	return min(max(runtime.NumCPU(), 2), maxWorkers)
}

func TestApplyLoadGovernorSamplerErrorKeepsBase(t *testing.T) {
	e := &Engine{
		loadAvgSampler: func(context.Context) (float64, error) {
			return 0, errors.New("loadavg unavailable")
		},
	}
	assert.Equal(t, 6, e.applyLoadGovernor(context.Background(), 6))
}

func TestApplyLoadGovernorShrinksUnderLoad(t *testing.T) {
	e := &Engine{
		loadAvgSampler: func(context.Context) (float64, error) {
			// Any realistic host: ratio far above 2.0.
			return 1_000, nil
		},
	}
	assert.Equal(t, 3, e.applyLoadGovernor(context.Background(), 6))
}

func TestApplyLoadGovernorDisabledByEnv(t *testing.T) {
	original := governorEnvLookup
	governorEnvLookup = func(string) string { return "0" }
	t.Cleanup(func() { governorEnvLookup = original })

	e := &Engine{
		loadAvgSampler: func(context.Context) (float64, error) {
			return 1_000, nil
		},
	}
	assert.Equal(t, 6, e.applyLoadGovernor(context.Background(), 6))
}

func TestGovernorEnabledToggle(t *testing.T) {
	original := governorEnvLookup
	t.Cleanup(func() { governorEnvLookup = original })

	tests := []struct {
		env  string
		want bool
	}{
		{env: "", want: true},
		{env: "1", want: true},
		{env: "true", want: true},
		{env: "0", want: false},
		{env: "false", want: false},
	}
	for _, tt := range tests {
		t.Run("env="+tt.env, func(t *testing.T) {
			governorEnvLookup = func(string) string { return tt.env }
			assert.Equal(t, tt.want, governorEnabled())
		})
	}
}

func TestWorkerCountWiring(t *testing.T) {
	t.Run("sampler error falls back to baseline", func(t *testing.T) {
		e := &Engine{
			loadAvgSampler: func(context.Context) (float64, error) {
				return 0, errors.New("platform without loadavg")
			},
		}
		assert.Equal(t, baselineWorkerCount(), e.workerCount())
	})

	t.Run("high load shrinks the pool", func(t *testing.T) {
		e := &Engine{
			loadAvgSampler: func(context.Context) (float64, error) {
				return 1_000, nil
			},
		}
		want := governedWorkerCount(baselineWorkerCount(), 1_000, runtime.NumCPU())
		require.Greater(t, baselineWorkerCount(), 1)
		assert.Equal(t, want, e.workerCount())
		assert.Less(t, e.workerCount(), baselineWorkerCount())
	})

	t.Run("override bypasses the governor", func(t *testing.T) {
		e := &Engine{
			workerCountOverride: 2,
			loadAvgSampler: func(context.Context) (float64, error) {
				return 1_000, nil
			},
		}
		assert.Equal(t, 2, e.workerCount())
	})

	t.Run("default sampler samples gopsutil without panic", func(t *testing.T) {
		e := &Engine{}
		got := e.workerCount()
		// Whatever the platform reports, the count must stay within the
		// production floor/cap band.
		assert.GreaterOrEqual(t, got, 1)
		assert.LessOrEqual(t, got, maxWorkers)
	})
}
