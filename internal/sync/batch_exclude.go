package sync

import (
	"fmt"
	"log"
	"path/filepath"

	"go.kenn.io/agentsview/internal/parser"
)

// batchExcludeMatcher applies the user's watch_exclude_patterns to batch
// discovery, so the batch lane and the watcher backends agree on which paths
// stay invisible. The verdict deliberately reuses shouldExcludeForRoot — the
// exact predicate the fsnotify and FSEvents backends run — over the same
// normalizeExcludePatterns normalization the watcher applies on receipt, so a
// pattern excludes a source on one lane if and only if it drops it on the
// other.
//
// Semantics are fail-closed: a pattern set that fails validation compiles to
// the exclude-everything matcher instead of an admitting one, so a broken
// configuration can never silently admit every source. Config loading rejects
// invalid patterns outright (the process refuses to start); the engine-level
// fallback covers engines built from unvalidated input.
type batchExcludeMatcher struct {
	// patterns are normalized and validated watcher exclude patterns. nil
	// means exclusion is not configured and every path is admitted.
	patterns []string
	// excludeAll is the fail-closed posture: every path is excluded.
	excludeAll bool
}

// newBatchExcludeMatcher validates patterns the same way the config loader
// does and returns the matcher batch discovery consults. An error means the
// set must not be trusted to admit paths.
func newBatchExcludeMatcher(patterns []string) (batchExcludeMatcher, error) {
	normalized := normalizeExcludePatterns(patterns)
	if len(normalized) == 0 {
		return batchExcludeMatcher{}, nil
	}
	for _, pattern := range normalized {
		if err := validateExcludePattern(pattern); err != nil {
			return batchExcludeMatcher{}, err
		}
	}
	return batchExcludeMatcher{patterns: normalized}, nil
}

// newFailClosedBatchExcludeMatcher builds the exclude-everything matcher used
// when a pattern set fails validation at engine construction. It never admits
// a path, so a broken configuration degrades to ingesting nothing rather than
// everything.
func newFailClosedBatchExcludeMatcher(cause error) batchExcludeMatcher {
	log.Printf(
		"sync: exclude patterns unusable, excluding every source until fixed: %v",
		cause,
	)
	return batchExcludeMatcher{excludeAll: true}
}

// validateExcludePattern reports whether filepath.Match can evaluate the
// pattern. filepath.Match's ErrBadPattern is a static property of the pattern,
// so probing once against an arbitrary string detects every malformed form.
func validateExcludePattern(pattern string) error {
	if _, err := filepath.Match(pattern, "agentsview-probe"); err != nil {
		return fmt.Errorf("watch_exclude_patterns: pattern %q: %w", pattern, err)
	}
	return nil
}

// enabled reports whether any exclusion decision is configured.
func (m batchExcludeMatcher) enabled() bool {
	return len(m.patterns) > 0 || m.excludeAll
}

// excludes reports whether path, relative to any of roots, matches the
// exclusion patterns. Paths outside every root are admitted: the watcher only
// excludes inside its watch roots, and batch discovery never enumerates
// outside the configured agent roots either.
func (m batchExcludeMatcher) excludes(path string, roots []string) bool {
	if !m.enabled() {
		return false
	}
	if m.excludeAll {
		return true
	}
	for _, root := range roots {
		if shouldExcludeForRoot(m.patterns, path, root) {
			return true
		}
	}
	return false
}

// excludesSource adapts excludes to a discovered provider source. Sources
// whose path cannot be resolved are admitted here so the existing
// missing-source handling reports them; every resolvable path under an
// excluded component stays invisible to the batch lane.
func (m batchExcludeMatcher) excludesSource(
	source parser.SourceRef, roots []string,
) bool {
	if !m.enabled() {
		return false
	}
	path := providerDiscoveredPath(source)
	if path == "" {
		return false
	}
	return m.excludes(path, roots)
}

// filterExcludedSources removes sources excluded by the matcher and returns
// the kept slice plus how many were dropped. The caller logs the dropped
// count, keeping failure reporting bounded to one line per provider pass.
func (m batchExcludeMatcher) filterExcludedSources(
	sources []parser.SourceRef, roots []string,
) ([]parser.SourceRef, int) {
	if !m.enabled() {
		return sources, 0
	}
	kept := sources[:0]
	excluded := 0
	for _, source := range sources {
		if m.excludesSource(source, roots) {
			excluded++
			continue
		}
		kept = append(kept, source)
	}
	return kept, excluded
}
