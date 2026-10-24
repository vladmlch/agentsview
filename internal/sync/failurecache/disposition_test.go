package failurecache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSource creates a file with the given contents and returns its current
// identity as the sync engine would record it.
func writeSource(t *testing.T, path, contents string) Identity {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return identityOf(t, path)
}

func identityOf(t *testing.T, path string) Identity {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err, "stating source")
	return Identity{
		MTimeNS: info.ModTime().UnixNano(),
		Size:    info.Size(),
	}
}

func TestClassify(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "session.jsonl")
	unchanged := writeSource(t, source, "line\n")

	changed := unchanged
	changed.Size++ // stats moved after the failure was recorded

	missingSource := filepath.Join(dir, "gone.jsonl")
	returnedSource := filepath.Join(dir, "returned.jsonl")
	writeSource(t, returnedSource, "back\n")

	tests := []struct {
		name     string
		path     string
		recorded Identity
		want     Disposition
	}{
		{
			name:     "stats unchanged since failure",
			path:     source,
			recorded: unchanged,
			want:     DispositionWaiting,
		},
		{
			name:     "stats changed since failure",
			path:     source,
			recorded: changed,
			want:     DispositionRetryStatsChanged,
		},
		{
			name:     "recorded parse failure whose file is gone",
			path:     missingSource,
			recorded: unchanged,
			want:     DispositionMissing,
		},
		{
			name:     "recorded missing source that is still gone",
			path:     missingSource,
			recorded: Identity{Missing: true},
			want:     DispositionMissing,
		},
		{
			name:     "recorded missing source that returned",
			path:     returnedSource,
			recorded: Identity{Missing: true, Fingerprint: "x"},
			want:     DispositionRetryReturned,
		},
		{
			name:     "empty path",
			path:     "",
			recorded: unchanged,
			want:     DispositionUnknown,
		},
		{
			name:     "remote source",
			path:     "s3://bucket/session.jsonl",
			recorded: unchanged,
			want:     DispositionUnknown,
		},
		{
			name:     "virtual source inside a container",
			path:     filepath.Join(dir, "state.vscdb") + "#row-7",
			recorded: unchanged,
			want:     DispositionUnknown,
		},
		{
			name:     "stat error other than not-exist",
			path:     filepath.Join(source, "under-a-file"),
			recorded: unchanged,
			want:     DispositionUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, Classify(test.path, test.recorded))
		})
	}
}

// A source recorded at the Unix epoch is a real file, not a missing one, so
// a zero mtime with Missing unset must not read as returned or missing.
func TestClassifyDistinguishesEpochMtimeFromMissing(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "epoch.jsonl")
	epoch := writeSource(t, source, "x\n")

	recorded := Identity{MTimeNS: 0, Size: epoch.Size}
	assert.Equal(t, DispositionRetryStatsChanged, Classify(source, recorded))
}
