/*******************************************************************************
 * Copyright (c) 2025 Synecdoque
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
 ***************************************************************************SDG*/

package main

import "time"

// OrderRequest is the body sent by the nurse (or any consumer) to create a maintenance order.
// Field names match the nurse's MaintenanceOrderEvent JSON tags so no translation is needed.
type OrderRequest struct {
	EquipmentID          string               `json:"equipmentId"`
	FunctionalLocation   string               `json:"functionalLocation,omitempty"`
	Plant                string               `json:"plant"`
	Description          string               `json:"description"`
	Priority             string               `json:"priority,omitempty"`
	MaintenanceOrderType string               `json:"maintenanceOrderType,omitempty"`
	PlannedStartTime     *time.Time           `json:"plannedStartTime,omitempty"`
	PlannedEndTime       *time.Time           `json:"plannedEndTime,omitempty"`
	Operations           []OrderOperation     `json:"operations,omitempty"`
}

// OrderOperation is a single work step within an order.
type OrderOperation struct {
	OperationID  string           `json:"operationId,omitempty"`
	Text         string           `json:"text"`
	WorkCenter   string           `json:"workCenter,omitempty"`
	Duration     float64          `json:"duration,omitempty"`
	DurationUnit string           `json:"durationUnit,omitempty"`
	Components   []OrderComponent `json:"components,omitempty"`
}

// OrderComponent is a material or part required for an operation.
type OrderComponent struct {
	Material        string  `json:"material"`
	Description     string  `json:"description,omitempty"`
	Quantity        float64 `json:"quantity"`
	Unit            string  `json:"unit,omitempty"`
	Plant           string  `json:"plant,omitempty"`
	StorageLocation string  `json:"storageLocation,omitempty"`
}

// OrderResponse is returned after a successful POST to /orders.
// Field names match the nurse's MaintenanceOrderResponse JSON tags.
type OrderResponse struct {
	MaintenanceOrder        string    `json:"maintenanceOrder"`
	MaintenanceNotification string    `json:"maintenanceNotification"`
	Status                  string    `json:"status"`
	Message                 string    `json:"message"`
	CreatedAt               time.Time `json:"createdAt"`
}

// Order is the in-memory representation of a maintenance order managed by the sapper.
type Order struct {
	ID           string
	Notification string
	Status       string // CRTD → REL → TECO
	CreatedAt    time.Time
	Request      OrderRequest
}

// CompletionEvent is POSTed to the consumer's monitor endpoint when an order reaches TECO.
// Field names match the nurse's MaintenanceDoneEvent JSON tags.
type CompletionEvent struct {
	OrderID         string     `json:"orderId"`
	Status          string     `json:"status"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	ActualWorkHours float64    `json:"actualWorkHours,omitempty"`
	Notes           string     `json:"notes,omitempty"`
}
