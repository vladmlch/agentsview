package main

import (
	"log"
	"os"
	"strings"
)

// Audit av-6: the sync-worker child inherits the CLI/daemon's scheduling
// class, so archive-scale indexing competes with interactive use for CPU and
// (on Linux) I/O. The worker child demotes itself at pass start instead of the
// parent setting SysProcAttr on the exec: the child runs the same binary, the
// inherited environment carries the toggle (launchSyncWorkerProcess passes
// os.Environ through), and the parent's process-group/signal semantics stay
// untouched. The daemon's rare in-process fallback (spawn failure) is
// deliberately not demoted: it would demote the whole interactive daemon with
// it, and the fallback is already the exceptional path.
const (
	// syncDemotionEnvVar disables the demotion when set to a false value;
	// demotion is on by default. A plain env var rather than a config field
	// because it only steers a best-effort syscall and must not fail config
	// loading when malformed.
	syncDemotionEnvVar = "AGENTSVIEW_SYNC_NICE"

	// syncDemotionNiceLevel is the nice increment applied to the worker
	// process. 10 matches the cass reference demotion; raising nice on the
	// calling process needs no privileges, so the common path cannot fail on
	// permissions.
	syncDemotionNiceLevel = 10
)

// syncDemotionEnabled reports whether the demotion is on for the given env
// value. Unset, empty, and unrecognized values keep the default (on) so a typo
// can never silently disable the demotion; only an explicit "0"/"false" (any
// case, optionally padded) opts out, mirroring the boolean env knobs in
// internal/config (e.g. AGENTSVIEW_DISABLE_UPDATE_CHECK).
func syncDemotionEnabled(envValue string) bool {
	switch strings.ToLower(strings.TrimSpace(envValue)) {
	case "0", "false":
		return false
	default:
		return true
	}
}

// syncDemoter applies the best-effort scheduling demotion through injectable
// platform hooks so tests can assert the attempted call and the swallowed
// error path without depending on platform permissions.
type syncDemoter struct {
	// setPriority lowers the calling process's CPU priority by prio; nil when
	// the platform has no setpriority binding.
	setPriority func(prio int) error
	// ioSetClass demotes the calling process's I/O scheduling class; nil off
	// Linux (darwin has no ionice equivalent).
	ioSetClass func() error
	// logf receives the best-effort failure diagnostics (debug log).
	logf func(format string, args ...any)
}

// demote lowers the process's CPU priority and, where available, its I/O
// priority. Every failure is logged once and swallowed: demotion is
// best-effort and must never fail or delay the sync pass, and platform
// differences (permissions, unsupported calls) are expected.
func (d syncDemoter) demote() {
	if d.setPriority != nil {
		if err := d.setPriority(syncDemotionNiceLevel); err != nil {
			d.logf(
				"sync worker demotion: nice %d unavailable, "+
					"continuing at inherited priority: %v",
				syncDemotionNiceLevel, err,
			)
		}
	}
	if d.ioSetClass != nil {
		if err := d.ioSetClass(); err != nil {
			d.logf(
				"sync worker demotion: I/O priority demotion unavailable, "+
					"continuing at inherited I/O priority: %v", err,
			)
		}
	}
}

// newSyncDemotion builds the production demoter from the platform hooks; a
// package var so tests can observe whether applySyncDemotion attempts the
// demotion under a given toggle value.
var newSyncDemotion = func() syncDemoter {
	demoter := platformSyncDemoter()
	demoter.logf = log.Printf
	return demoter
}

// applySyncDemotion demotes the current process for heavy ingest work when the
// env toggle permits, best-effort.
func applySyncDemotion() {
	if !syncDemotionEnabled(os.Getenv(syncDemotionEnvVar)) {
		return
	}
	newSyncDemotion().demote()
}

// demoteSyncWorkerPass indirects applySyncDemotion so tests can observe or
// suppress the worker child's demotion without depending on platform
// permissions, mirroring the launchSyncWorker seam.
var demoteSyncWorkerPass = applySyncDemotion
