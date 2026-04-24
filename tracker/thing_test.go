/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
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
 *   Franziska Sievert - initial implementation
 *   Jan A. van Deventer, Luleå - modernized for current mbaigo
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// openTestDB opens an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *Traits {
	t.Helper()
	db, closeDB, err := openDB_memory()
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(closeDB)
	return &Traits{db: db}
}

// TestInsertOrder verifies that a new order receives a positive OrderNumber.
func TestInsertOrder(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		Name:             "Alice",
		Email:            "alice@example.com",
		Height:           15.0,
		Depth:            5.0,
		Roughness:        3,
		OrderedTimestamp: time.Now(),
		ProductionLine:   "LineA",
	}
	order.NewForm()
	id, err := InsertOrder(tr.db, order)
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive order number, got %d", id)
	}
}

// TestGetOrder verifies that an inserted order can be retrieved.
func TestGetOrder(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		Name: "Bob", Email: "bob@example.com",
		Height: 10, Depth: 4, Roughness: 2,
		OrderedTimestamp: time.Now(), ProductionLine: "LineB",
	}
	order.NewForm()
	id, err := InsertOrder(tr.db, order)
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}

	got, err := GetOrder(tr.db, id)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.Name != "Bob" {
		t.Errorf("expected Name \"Bob\", got %q", got.Name)
	}
	if got.OrderNumber != id {
		t.Errorf("expected OrderNumber %d, got %d", id, got.OrderNumber)
	}
}

// TestGetOrder_NotFound verifies that retrieving a non-existent order returns an error.
func TestGetOrder_NotFound(t *testing.T) {
	tr := openTestDB(t)
	_, err := GetOrder(tr.db, 9999)
	if err == nil {
		t.Error("expected error for missing order, got nil")
	}
}

// TestUpdateOrder verifies that an existing order can be updated.
func TestUpdateOrder(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		Name: "Carol", Email: "carol@example.com",
		Height: 12, Depth: 3, Roughness: 1,
		OrderedTimestamp: time.Now(), ProductionLine: "LineC",
	}
	order.NewForm()
	id, err := InsertOrder(tr.db, order)
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}

	order.OrderNumber = id
	order.ProductionLine = "LineX"
	if err := UpdateOrder(tr.db, order); err != nil {
		t.Fatalf("UpdateOrder: %v", err)
	}

	got, err := GetOrder(tr.db, id)
	if err != nil {
		t.Fatalf("GetOrder after update: %v", err)
	}
	if got.ProductionLine != "LineX" {
		t.Errorf("expected ProductionLine \"LineX\", got %q", got.ProductionLine)
	}
}

// TestUpdateOrder_NotFound verifies that updating a non-existent order returns an error.
func TestUpdateOrder_NotFound(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{OrderNumber: 9999, Name: "Ghost"}
	order.NewForm()
	err := UpdateOrder(tr.db, order)
	if err == nil {
		t.Error("expected error for missing order, got nil")
	}
}

// TestOrderHandler_GET returns the order as JSON when both id and email match.
func TestOrderHandler_GET(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		Name: "Dave", Email: "dave@example.com",
		Height: 8, Depth: 2, Roughness: 4,
		OrderedTimestamp: time.Now(), ProductionLine: "LineD",
	}
	order.NewForm()
	id, err := InsertOrder(tr.db, order)
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/order", nil)
	q := req.URL.Query()
	q.Set("id", intStr(id))
	q.Set("email", "dave@example.com")
	req.URL.RawQuery = q.Encode()

	w := httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
}

// TestOrderHandler_GET_MissingParams returns 400 when id or email is absent.
func TestOrderHandler_GET_MissingParams(t *testing.T) {
	tr := openTestDB(t)

	// Neither parameter.
	req := httptest.NewRequest(http.MethodGet, "/order", nil)
	w := httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (no params), got %d", w.Result().StatusCode)
	}

	// id only — email missing.
	req = httptest.NewRequest(http.MethodGet, "/order?id=1", nil)
	w = httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (id only), got %d", w.Result().StatusCode)
	}

	// email only — id missing.
	req = httptest.NewRequest(http.MethodGet, "/order?email=x@x.com", nil)
	w = httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 (email only), got %d", w.Result().StatusCode)
	}
}

// TestGetOrderByIDAndEmail verifies correct and incorrect email lookup behavior.
func TestGetOrderByIDAndEmail(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		Name: "Eve", Email: "eve@example.com",
		Height: 7, Depth: 2, Roughness: 32,
		OrderedTimestamp: time.Now(), ProductionLine: "LineE",
	}
	order.NewForm()
	id, err := InsertOrder(tr.db, order)
	if err != nil {
		t.Fatalf("InsertOrder: %v", err)
	}

	// Correct credentials.
	got, err := GetOrderByIDAndEmail(tr.db, id, "eve@example.com")
	if err != nil {
		t.Fatalf("GetOrderByIDAndEmail: %v", err)
	}
	if got.Name != "Eve" {
		t.Errorf("expected Name \"Eve\", got %q", got.Name)
	}

	// Wrong email — must return error, not the record.
	_, err = GetOrderByIDAndEmail(tr.db, id, "wrong@example.com")
	if err == nil {
		t.Error("expected error for wrong email, got nil")
	}
}

// TestOrderHandler_POST creates a new order and returns 200 with the assigned number.
func TestOrderHandler_POST(t *testing.T) {
	tr := openTestDB(t)
	order := &PenHolderOrder_v1{
		OrderNumber: 0, // new order sentinel
		Name:        "Eve", Email: "eve@example.com",
		Height: 9, Depth: 1, Roughness: 5,
		OrderedTimestamp: time.Now(), ProductionLine: "LineE",
	}
	order.NewForm()
	body, _ := json.Marshal(order)

	req := httptest.NewRequest(http.MethodPost, "/order", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Result().StatusCode, w.Body.String())
	}
}

// TestOrderHandler_InvalidMethod returns 405 for DELETE.
func TestOrderHandler_InvalidMethod(t *testing.T) {
	tr := openTestDB(t)
	req := httptest.NewRequest(http.MethodDelete, "/order", nil)
	w := httptest.NewRecorder()
	tr.orderHandler(w, req)
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Result().StatusCode)
	}
}

// TestServing_InvalidPath returns 400 for an unknown service path.
func TestServing_InvalidPath(t *testing.T) {
	tr := openTestDB(t)
	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	w := httptest.NewRecorder()
	serving(tr, w, req, "unknown")
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Result().StatusCode)
	}
}

// TestPenHolderOrder_v1_FormVersion verifies that NewForm sets the version.
func TestPenHolderOrder_v1_FormVersion(t *testing.T) {
	var f PenHolderOrder_v1
	f.NewForm()
	if f.FormVersion() != "PenHolderOrder_v1" {
		t.Errorf("expected version \"PenHolderOrder_v1\", got %q", f.FormVersion())
	}
}

// intStr converts an int to a decimal string without fmt dependency in tests.
func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
