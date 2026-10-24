//go:build !darwin && !linux

package main

// platformSyncDemoter is a no-op on platforms without a Go setpriority
// binding (e.g. windows): both hooks stay nil and demote skips them, so the
// toggle parsing and logging paths remain exercisable everywhere.
func platformSyncDemoter() syncDemoter {
	return syncDemoter{}
}
