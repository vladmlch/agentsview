package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/config"
)

// recordingDemoter builds a demoter whose hooks record calls, for asserting
// that demote attempts the priority calls without depending on platform
// permissions.
func recordingDemoter(logs *[]string) (syncDemoter, *int, *int) {
	setPriorityCalls, ioClassCalls := 0, 0
	return syncDemoter{
		setPriority: func(int) error {
			setPriorityCalls++
			return nil
		},
		ioSetClass: func() error {
			ioClassCalls++
			return nil
		},
		logf: func(format string, args ...any) {
			*logs = append(*logs, fmt.Sprintf(format, args...))
		},
	}, &setPriorityCalls, &ioClassCalls
}

func TestSyncDemotionEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want bool
	}{
		{name: "unset defaults on", env: "", want: true},
		{name: "zero disables", env: "0", want: false},
		{name: "false disables", env: "false", want: false},
		{name: "case-insensitive false disables", env: "FALSE", want: false},
		{name: "padded zero disables", env: " 0 ", want: false},
		{name: "one keeps on", env: "1", want: true},
		{name: "true keeps on", env: "true", want: true},
		{name: "unrecognized value stays on", env: "nope", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, syncDemotionEnabled(tt.env))
		})
	}
}

func TestSyncDemoterDemote(t *testing.T) {
	t.Run("attempts nice demotion at the configured level", func(t *testing.T) {
		var got []int
		demoter := syncDemoter{
			setPriority: func(prio int) error {
				got = append(got, prio)
				return nil
			},
			logf: func(string, ...any) {
				t.Error("successful demotion must not log")
			},
		}
		demoter.demote()
		require.Equal(t, []int{syncDemotionNiceLevel}, got)
	})

	t.Run("attempts io demotion when the platform provides it", func(t *testing.T) {
		var logs []string
		demoter, setPriorityCalls, ioClassCalls := recordingDemoter(&logs)
		demoter.demote()
		assert.Equal(t, 1, *setPriorityCalls)
		assert.Equal(t, 1, *ioClassCalls)
		assert.Empty(t, logs)
	})

	t.Run("nil hooks are skipped without logging", func(t *testing.T) {
		var logs []string
		demoter := syncDemoter{
			logf: func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		}
		demoter.demote()
		assert.Empty(t, logs)
	})

	t.Run("swallows setpriority errors after logging once", func(t *testing.T) {
		var logs []string
		demoter := syncDemoter{
			setPriority: func(int) error {
				return errors.New("operation not permitted")
			},
			logf: func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		}
		assert.NotPanics(t, demoter.demote)
		require.Len(t, logs, 1)
		assert.Contains(t, logs[0], "sync worker demotion")
		assert.Contains(t, logs[0], "operation not permitted")
	})

	t.Run("swallows io demotion errors after logging once", func(t *testing.T) {
		var logs []string
		demoter := syncDemoter{
			setPriority: func(int) error { return nil },
			ioSetClass: func() error {
				return errors.New("invalid argument")
			},
			logf: func(format string, args ...any) {
				logs = append(logs, fmt.Sprintf(format, args...))
			},
		}
		assert.NotPanics(t, demoter.demote)
		require.Len(t, logs, 1)
		assert.Contains(t, logs[0], "I/O priority demotion unavailable")
	})
}

func TestApplySyncDemotionToggle(t *testing.T) {
	restore := newSyncDemotion
	t.Cleanup(func() { newSyncDemotion = restore })
	attempts := 0
	newSyncDemotion = func() syncDemoter {
		return syncDemoter{
			setPriority: func(prio int) error {
				attempts++
				assert.Equal(t, syncDemotionNiceLevel, prio)
				return nil
			},
			logf: func(string, ...any) {},
		}
	}

	t.Run("disabled skips the demotion entirely", func(t *testing.T) {
		t.Setenv(syncDemotionEnvVar, "0")
		applySyncDemotion()
		assert.Equal(t, 0, attempts)
	})

	t.Run("enabled attempts the nice demotion", func(t *testing.T) {
		t.Setenv(syncDemotionEnvVar, "1")
		applySyncDemotion()
		assert.Equal(t, 1, attempts)
	})

	t.Run("unset defaults to attempting the demotion", func(t *testing.T) {
		// Empty equals unset for the parser and shields the test from an
		// ambient AGENTSVIEW_SYNC_NICE in the surrounding environment.
		t.Setenv(syncDemotionEnvVar, "")
		applySyncDemotion()
		assert.Equal(t, 2, attempts)
	})
}

func TestRunSyncWorkerContextDemotesAtPassStart(t *testing.T) {
	restore := demoteSyncWorkerPass
	t.Cleanup(func() { demoteSyncWorkerPass = restore })
	var demotions int
	demoteSyncWorkerPass = func() { demotions++ }

	// An unknown mode fails after the demotion seam fires, without touching
	// any database: the cheapest full-fidelity path through the child's pass
	// entry point.
	err := runSyncWorkerContext(
		context.Background(), config.Config{},
		syncWorkerRequest{Mode: "no-such-mode"}, io.Discard,
	)
	require.Error(t, err)
	assert.Equal(t, 1, demotions)
}
