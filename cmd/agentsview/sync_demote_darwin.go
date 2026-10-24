//go:build darwin

package main

import "syscall"

// platformSyncDemoter demotes CPU priority via setpriority(PRIO_PROCESS, 0,
// nice). Raising nice on the calling process requires no privileges, so the
// common path succeeds. macOS has no ionice equivalent; the I/O hook stays
// nil and is skipped silently.
func platformSyncDemoter() syncDemoter {
	return syncDemoter{
		setPriority: func(prio int) error {
			return syscall.Setpriority(syscall.PRIO_PROCESS, 0, prio)
		},
	}
}
