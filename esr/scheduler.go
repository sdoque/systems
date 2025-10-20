/*******************************************************************************
 * Copyright (c) 2024 Synecdoque
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, subject to the following conditions:
 *
 * The software is licensed under the MIT License. See the LICENSE file in this repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"sync"
	"time"
)

// Scheduler struct type with the list and three channels
type Scheduler struct {
	taskMap map[int]*time.Timer // list elements has id, timer
	mu      sync.Mutex
}

// Returns a scheduler with an empty task map
func NewScheduler() *Scheduler {
	return &Scheduler{
		taskMap: make(map[int]*time.Timer),
		mu:      sync.Mutex{},
	}
}

// AddTask adds a task to the task map and starts a timer for its job, when timer is done it runs the job in a goroutine
// It's up to the caller to ensure that the deadline is not before time.Now()
func (s *Scheduler) AddTask(deadline time.Time, job func(), id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer, exists := s.taskMap[id]
	if exists {
		timer.Stop()
	}
	t := time.AfterFunc(time.Until(deadline), job)
	s.taskMap[id] = t
}

// RemoveTask removes a scheduled job and deletes the task from the task map
func (s *Scheduler) RemoveTask(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	timer, exists := s.taskMap[id]
	if !exists {
		return false
	}
	timer.Stop()
	delete(s.taskMap, id)
	return true
}

// Stop() loops through the task map and turns off the timer for each tasks job
func (s *Scheduler) Stop() (counter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, timer := range s.taskMap {
		timer.Stop()
		counter++
	}
	s.taskMap = make(map[int]*time.Timer)
	return
}
