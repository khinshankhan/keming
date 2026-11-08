//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

func TestLowerPriorityLowersEveryThread(t *testing.T) {
	lowerPriority()
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) < 2 {
		t.Fatalf("only %d thread, the test needs several", len(tasks))
	}
	for _, task := range tasks {
		stat, err := os.ReadFile("/proc/self/task/" + task.Name() + "/stat")
		if err != nil {
			continue // the thread ended
		}
		// Fields after the command name, which is in parentheses; nice is
		// the 19th field overall, the 17th after it.
		fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:]))
		if nice := fields[16]; nice != "10" {
			t.Errorf("thread %s has nice %s, want 10", task.Name(), nice)
		}
	}
}
