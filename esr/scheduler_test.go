package main

import (
	"testing"
	"time"
)

// -------------------- //
// Tests for scheduler
// -------------------- //

func TestAddTask(t *testing.T) {
	sched := NewScheduler()
	now := time.Now()
	ch := make(chan int)
	// case: ensure chrono order
	sched.AddTask(now.Add(2*time.Second), func() { ch <- 0 }, 0)
	sched.AddTask(now.Add(5*time.Millisecond), func() { ch <- 1 }, 1)

	select {
	case id := <-ch:
		if id != 1 {
			t.Errorf("Expected 1 from channel, got %d", id)
		}
	case <-time.After(50 * time.Millisecond):
		t.Errorf("Chronological order test timed out")
	}
	sched.Stop()

	sched.AddTask(now.Add(2*time.Second), func() { ch <- 0 }, 0)
	sched.AddTask(now.Add(25*time.Millisecond), func() { ch <- 1 }, 0)

	select {
	case id := <-ch:
		if id != 1 {
			t.Errorf("Expected 1 from channel, got %d", id)
		}
	case <-time.After(50 * time.Millisecond):
		t.Errorf("Duplicate ID test timed out")
	}
	sched.Stop()
}

func TestRemoveTask(t *testing.T) {
	// TODO: Similar to AddTask for the PR comment

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
	// TODO: Some kind of counter
	sched := NewScheduler()
	now := time.Now()

	// Add some tasks and make sure Stop() works as intended
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)
	sched.AddTask(now.Add(25*time.Second), func() {}, 1)
	sched.AddTask(now.Add(25*time.Second), func() {}, 2)
	sched.AddTask(now.Add(25*time.Second), func() {}, 3)
	sched.Stop()

	if len(sched.taskMap) > 0 {
		t.Errorf("Expected taskMap to be empty, has %d keys", len(sched.taskMap))
	}
}
