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
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
	_ "modernc.org/sqlite"
)

//-------------------------------------Define the unit asset

// Traits holds the runtime state for the order tracker unit asset.
type Traits struct {
	owner    *components.System  `json:"-"`
	cervices components.Cervices `json:"-"`
	db       *sql.DB             `json:"-"`
}

//-------------------------------------Instantiate a unit asset template

// initTemplate returns a UnitAsset with default values used to seed systemconfig.json.
func initTemplate() *components.UnitAsset {
	orderService := components.Service{
		Definition:  "order",
		SubPath:     "order",
		Details:     map[string][]string{"Forms": {"PenHolderOrder_v1"}},
		RegPeriod:   60,
		Description: "create an order record (POST), update it (PUT), or retrieve it (GET ?id=N)",
	}

	return &components.UnitAsset{
		Name:    "product",
		Mission: "track_orders",
		Details: map[string][]string{"Status": {"Evaluation"}},
		ServicesMap: components.Services{
			orderService.SubPath: &orderService,
		},
		Traits: &Traits{},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its database connection and cervices.
func newResource(uac usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	db, closeDB, err := openDB()
	if err != nil {
		log.Fatalf("tracker: error opening database: %v\n", err)
	}

	sProtocols := components.SProtocols(sys.Husk.ProtoPort)

	// Cervice: forward newly inserted orders to a downstream processing system.
	addorderCer := &components.Cervice{
		Definition: "addorder",
		Protos:     sProtocols,
		Nodes:      make(map[string][]components.NodeInfo),
	}

	t := &Traits{
		owner: sys,
		db:    db,
		cervices: components.Cervices{
			addorderCer.Definition: addorderCer,
		},
	}

	ua := &components.UnitAsset{
		Name:        uac.Name,
		Mission:     uac.Mission,
		Owner:       sys,
		Details:     uac.Details,
		ServicesMap: usecases.MakeServiceMap(uac.Services),
		CervicesMap: t.cervices,
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		closeDB()
		log.Println("tracker: database connection closed")
	}
}

//-------------------------------------Order form

// PenHolderOrder_v1 is the exchanged form for pen holder orders.
type PenHolderOrder_v1 struct {
	OrderNumber        int       `json:"order_number"`
	Name               string    `json:"name"`
	Email              string    `json:"email"`
	Height             float64   `json:"height"`
	Depth              float64   `json:"depth"`
	Roughness          int       `json:"roughness"`
	OrderedTimestamp   time.Time `json:"timestamp"`
	CompletedTimestamp time.Time `json:"completed_timestamp"`
	ProductionLine     string    `json:"production_line"`
	PeppolID           string    `json:"peppol_id"`
	Version            string    `json:"version"`
}

func (f *PenHolderOrder_v1) NewForm() forms.Form {
	f.Version = "PenHolderOrder_v1"
	return f
}

func (f *PenHolderOrder_v1) FormVersion() string {
	return f.Version
}

func init() {
	forms.FormTypeMap["PenHolderOrder_v1"] = reflect.TypeOf(PenHolderOrder_v1{})
}

//-------------------------------------Database

// openDB opens (or creates) the SQLite orders database at orders.db.
func openDB() (*sql.DB, func(), error) {
	return openDBAt("orders.db")
}

// openDB_memory opens an in-memory SQLite database; used by tests.
func openDB_memory() (*sql.DB, func(), error) {
	return openDBAt(":memory:")
}

func openDBAt(path string) (*sql.DB, func(), error) {
	if path != ":memory:" {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Println("tracker: database does not exist, creating...")
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening database: %w", err)
	}

	if err := createTableIfNotExists(db); err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("creating table: %w", err)
	}

	fmt.Println("tracker: database ready")
	return db, func() { db.Close() }, nil
}

// createTableIfNotExists creates the PenHolderOrders table if it does not already exist,
// and adds the PeppolID column to pre-existing databases via a migration.
func createTableIfNotExists(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS PenHolderOrders (
			OrderNumber        INTEGER PRIMARY KEY AUTOINCREMENT,
			Name               TEXT    NOT NULL,
			Email              TEXT    NOT NULL,
			Height             REAL    NOT NULL,
			Depth              REAL    NOT NULL,
			Roughness          INTEGER NOT NULL,
			OrderedTimestamp   DATETIME NOT NULL,
			CompletedTimestamp DATETIME,
			ProductionLine     TEXT    NOT NULL,
			PeppolID           TEXT    NOT NULL DEFAULT '',
			Version            TEXT    NOT NULL
		);`)
	if err != nil {
		return fmt.Errorf("creating table: %w", err)
	}
	// Migration: add PeppolID to databases created before this column existed.
	// The error is intentionally ignored — it fails harmlessly if the column is already present.
	db.Exec(`ALTER TABLE PenHolderOrders ADD COLUMN PeppolID TEXT NOT NULL DEFAULT '';`) //nolint:errcheck
	return nil
}

// InsertOrder inserts a new order and returns the assigned order number.
func InsertOrder(db *sql.DB, order *PenHolderOrder_v1) (int, error) {
	result, err := db.Exec(`
		INSERT INTO PenHolderOrders
			(Name, Email, Height, Depth, Roughness,
			 OrderedTimestamp, CompletedTimestamp, ProductionLine, PeppolID, Version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		order.Name, order.Email, order.Height, order.Depth, order.Roughness,
		order.OrderedTimestamp, order.CompletedTimestamp,
		order.ProductionLine, order.PeppolID, order.Version,
	)
	if err != nil {
		return 0, fmt.Errorf("inserting order: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("retrieving insert ID: %w", err)
	}
	return int(id), nil
}

// UpdateOrder updates an existing order record identified by its order number.
func UpdateOrder(db *sql.DB, order *PenHolderOrder_v1) error {
	result, err := db.Exec(`
		UPDATE PenHolderOrders
		SET Name=?, Email=?, Height=?, Depth=?, Roughness=?,
		    OrderedTimestamp=?, CompletedTimestamp=?, ProductionLine=?, PeppolID=?, Version=?
		WHERE OrderNumber=?;`,
		order.Name, order.Email, order.Height, order.Depth, order.Roughness,
		order.OrderedTimestamp, order.CompletedTimestamp,
		order.ProductionLine, order.PeppolID, order.Version,
		order.OrderNumber,
	)
	if err != nil {
		return fmt.Errorf("updating order: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("no order found with OrderNumber %d", order.OrderNumber)
	}
	return nil
}

// GetOrder retrieves a single order by its order number.
func GetOrder(db *sql.DB, orderNumber int) (*PenHolderOrder_v1, error) {
	row := db.QueryRow(`
		SELECT OrderNumber, Name, Email, Height, Depth, Roughness,
		       OrderedTimestamp, CompletedTimestamp, ProductionLine, PeppolID, Version
		FROM PenHolderOrders WHERE OrderNumber=?;`, orderNumber)

	var o PenHolderOrder_v1
	err := row.Scan(
		&o.OrderNumber, &o.Name, &o.Email, &o.Height, &o.Depth, &o.Roughness,
		&o.OrderedTimestamp, &o.CompletedTimestamp, &o.ProductionLine, &o.PeppolID, &o.Version,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("order %d not found", orderNumber)
	}
	if err != nil {
		return nil, fmt.Errorf("querying order: %w", err)
	}
	return &o, nil
}

// GetOrderByIDAndEmail retrieves a single order matching both order number and email address.
// A deliberately vague error is returned when no record matches, to prevent enumeration attacks.
func GetOrderByIDAndEmail(db *sql.DB, orderNumber int, email string) (*PenHolderOrder_v1, error) {
	row := db.QueryRow(`
		SELECT OrderNumber, Name, Email, Height, Depth, Roughness,
		       OrderedTimestamp, CompletedTimestamp, ProductionLine, PeppolID, Version
		FROM PenHolderOrders WHERE OrderNumber=? AND Email=?;`, orderNumber, email)

	var o PenHolderOrder_v1
	err := row.Scan(
		&o.OrderNumber, &o.Name, &o.Email, &o.Height, &o.Depth, &o.Roughness,
		&o.OrderedTimestamp, &o.CompletedTimestamp, &o.ProductionLine, &o.PeppolID, &o.Version,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("no order found with that number and email")
	}
	if err != nil {
		return nil, fmt.Errorf("querying order: %w", err)
	}
	return &o, nil
}
