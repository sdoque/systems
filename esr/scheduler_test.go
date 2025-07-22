package main

import (
	"testing"
	"time"
)

// --------------------------------------------------------------------------- //
// Help functions and structs to test the add part of ****
// --------------------------------------------------------------------------- //

type addTaskParams struct {
	setup func() *Scheduler
}

func TestAddTask(t *testing.T) {
	sched := NewScheduler()
	now := time.Now()

	// Add the task
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)

	if _, exists := sched.taskMap[0]; !exists {
		t.Errorf("Task was not present")
	}
}

func TestRemoveTask(t *testing.T) {
	// Case: task exists
	sched := NewScheduler()
	now := time.Now()

	// Add the task and then remove it
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)
	sched.RemoveTask(0)

	if _, exists := sched.taskMap[0]; exists {
		t.Errorf("Unexpected task exists in taskMap[0]")
	}

	// Case: task doesn't exist
	sched = NewScheduler()
	// Add the task and then remove it
	sched.RemoveTask(0)

	if _, exists := sched.taskMap[0]; exists {
		t.Errorf("Unexpected task exists in taskMap[0]")
	}
}

func TestStop(t *testing.T) {
	sched := NewScheduler()
	now := time.Now()

	// Add the task and then remove it
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)
	sched.AddTask(now.Add(25*time.Second), func() {}, 1)
	sched.AddTask(now.Add(25*time.Second), func() {}, 2)
	sched.AddTask(now.Add(25*time.Second), func() {}, 3)
	sched.Stop()

	if len(sched.taskMap) > 0 {
		t.Errorf("Expected taskMap to be empty, has %d keys", len(sched.taskMap))
	}
}
