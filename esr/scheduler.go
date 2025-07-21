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

import "time"

// Scheduler struct type with the list and three channels
type Scheduler struct {
	taskMap map[int]*time.Timer // list elements has id, timer
}

// NewScheduler creates a new scheduler
func NewScheduler() *Scheduler {
	return &Scheduler{
		taskMap: make(map[int]*time.Timer),
	}
}

// AddTask adds a task to the queue with its deadline
func (s *Scheduler) AddTask(deadline time.Time, job func(), id int) {
	timer, exists := s.taskMap[id]
	if exists {
		timer.Stop()
	}
	t := time.AfterFunc(time.Until(deadline), job)
	s.taskMap[id] = t
}

// RemoveTask removes a scheduled task
func (s *Scheduler) RemoveTask(id int) {
	timer, exists := s.taskMap[id]
	if !exists {
		return
	}
	timer.Stop()
	delete(s.taskMap, id)
}

func (s *Scheduler) Stop() {
	for _, value := range s.taskMap {
		value.Stop()
	}
	s.taskMap = make(map[int]*time.Timer)
}
