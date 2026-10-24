//go:build linux

package main

import "syscall"

// ioprio_set(2) request encoding: the value packs the class and level, and
// who selects the target. There is no Go binding, so the raw syscall is used.
const (
	// ioprioWhoProcess targets the calling process (pid 0 below).
	ioprioWhoProcess = 1
	// ioprioClassShift positions the class bits above the level bits.
	ioprioClassShift = 13
	// ioprioClassBE is IOPRIO_CLASS_BE: best-effort scheduling, the class
	// normal interactive I/O runs in. ioprioLevelLowest puts the worker at
	// the back of that class instead of IOPRIO_CLASS_IDLE: the idle class
	// only runs when no other I/O is queued, so under constant disk activity
	// the demoted pass could starve for minutes while the daemon blocks on
	// its terminal result.
	ioprioClassBE     = 2
	ioprioLevelLowest = 7
)

// platformSyncDemoter demotes CPU priority via setpriority(PRIO_PROCESS, 0,
// nice) — raising nice on the calling process needs no privileges — and I/O
// priority via ioprio_set(IOPRIO_WHO_PROCESS, 0, BE level 7), the equivalent
// of `ionice -c2 -n7`.
func platformSyncDemoter() syncDemoter {
	return syncDemoter{
		setPriority: func(prio int) error {
			return syscall.Setpriority(syscall.PRIO_PROCESS, 0, prio)
		},
		ioSetClass: func() error {
			value := ioprioClassBE<<ioprioClassShift | ioprioLevelLowest
			_, _, errno := syscall.Syscall(
				syscall.SYS_IOPRIO_SET,
				uintptr(ioprioWhoProcess), 0, uintptr(value),
			)
			if errno != 0 {
				return errno
			}
			return nil
		},
	}
}
