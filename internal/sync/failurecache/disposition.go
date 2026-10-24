package failurecache

import (
	"errors"
	"os"
	"strings"

	"go.kenn.io/agentsview/internal/parser"
)

// Disposition is what the sync engine will do with a recorded source failure
// on its next pass, derived from the source file's current filesystem state
// without running a sync.
type Disposition string

const (
	// DispositionWaiting: the source still exists and its mtime and size
	// match the recorded identity, so the engine will keep skipping it.
	DispositionWaiting Disposition = "waiting"
	// DispositionRetryStatsChanged: the source exists but its mtime or size
	// differ from the recorded identity, so the engine will parse it again.
	DispositionRetryStatsChanged Disposition = "retry-stats-changed"
	// DispositionRetryReturned: the failure was recorded while the source
	// did not exist, and the file is back. The engine clears the entry and
	// parses the source again.
	DispositionRetryReturned Disposition = "retry-file-returned"
	// DispositionMissing: the source file does not exist now.
	DispositionMissing Disposition = "missing"
	// DispositionUnknown: the disposition cannot be derived from the local
	// filesystem: remote or virtual sources, or a stat error other than
	// not-exist.
	DispositionUnknown Disposition = "unknown"
)

// Classify derives the disposition of the failure recorded for path. It
// applies the same change predicate Check applies — identity equality over
// mtime, size, and fingerprint — restricted to the fields observable without
// the sync engine's providers: the mtime and size from lstat. The
// fingerprint half of the identity is only recomputed at sync time, so a
// DispositionWaiting verdict means the stats are unchanged; the engine
// remains authoritative. Composite sources whose identity spans several
// files can report DispositionRetryStatsChanged while their combined
// identity is in fact unchanged; that direction is safe because the engine
// re-verifies before skipping.
func Classify(path string, recorded Identity) Disposition {
	if path == "" || strings.HasPrefix(path, "s3://") {
		return DispositionUnknown
	}
	if _, _, virtual := parser.ParseVirtualSourcePath(path); virtual {
		return DispositionUnknown
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DispositionMissing
		}
		return DispositionUnknown
	}
	if recorded.Missing {
		return DispositionRetryReturned
	}
	if info.ModTime().UnixNano() == recorded.MTimeNS && info.Size() == recorded.Size {
		return DispositionWaiting
	}
	return DispositionRetryStatsChanged
}
