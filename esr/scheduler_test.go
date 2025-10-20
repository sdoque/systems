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

	// Case: ID is reused between tasks
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
	// Case: task exists
	sched := NewScheduler()
	now := time.Now()

	// Add the task and then remove it, function should return true since it removed a task
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)

	if removed := sched.RemoveTask(0); removed != true {
		t.Errorf("Expected function to return true")
	}

	if _, exists := sched.taskMap[0]; exists {
		t.Errorf("Expected no element in taskMap[0]")
	}

	// Case: task doesn't exist, function should return false since there was no task to remove
	sched = NewScheduler()
	// Add the task and then remove it
	if removed := sched.RemoveTask(0); removed == true {
		t.Errorf("Expected function to return false")
	}
}

func TestStop(t *testing.T) {
	sched := NewScheduler()
	now := time.Now()

	// Add some tasks and make sure Stop() works as intended
	sched.AddTask(now.Add(25*time.Second), func() {}, 0)
	sched.AddTask(now.Add(25*time.Second), func() {}, 1)
	sched.AddTask(now.Add(25*time.Second), func() {}, 2)
	sched.AddTask(now.Add(25*time.Second), func() {}, 3)
	count := sched.Stop()

	if count < 4 {
		t.Errorf("Expected scheduler to turn off 4 tasks, got %d", count)
	}
}
