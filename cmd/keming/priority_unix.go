//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
)

// niceness is how far keming lowers its CPU priority.
const niceness = 10

// lowerPriority lowers the priority of every thread in the process. On Linux
// setpriority changes only the thread it names, so each is lowered by id;
// threads started later inherit it from the thread that starts them.
func lowerPriority() {
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		syscall.Setpriority(syscall.PRIO_PROCESS, 0, niceness)
		return
	}
	for _, t := range tasks {
		if tid, err := strconv.Atoi(t.Name()); err == nil {
			syscall.Setpriority(syscall.PRIO_PROCESS, tid, niceness)
		}
	}
}
