/*******************************************************************************
 * Copyright (c) 2023 Jan van Deventer
 *
 * All rights reserved. This program and the accompanying materials
 * are made available under the terms of the Eclipse Public License v2.0
 * which accompanies this distribution, and is available at
 * http://www.eclipse.org/legal/epl-2.0/
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/forms"
	_ "modernc.org/sqlite"
)

// createDB initializes the database by deleting any existing file and creating a new one.
func createDB() (*sql.DB, error) {
	os.Remove("serviceRegistry.db")
	fmt.Println("Creating serviceRegistry.db...")
	file, err := os.Create("serviceRegistry.db")
	if err != nil {
		return nil, err
	}
	file.Close()
	fmt.Println("serviceRegistry.db created")
	db, err := sql.Open("sqlite", "./serviceRegistry.db")
	if err != nil {
		return nil, fmt.Errorf("error opening database: %w", err)
	}

	if err = createTables(db); err != nil {
		return nil, err
	}
	fmt.Println("Database Ready")
	return db, nil
}

// createTables creates the necessary tables in the SQLite database.
func createTables(db *sql.DB) error {
	tableStatements := []string{
		`PRAGMA journal_mode = WAL;`,
		`CREATE TABLE Services (
			Id INTEGER PRIMARY KEY,
			Definition TEXT,
			SystemName TEXT,
			Certificate TEXT,
			SubPath TEXT,
			Version TEXT,
			Created TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			Updated TEXT,
			RegLife TEXT,
			EndOfValidity TIMESTAMP,
			SubscribeAble BOOLEAN,
			ACost REAL,
			CUnit TEXT
		);`,
		`CREATE TABLE IPAddresses (
			Id INTEGER PRIMARY KEY,
			IPAddress TEXT
		);`,
		`CREATE TABLE ProtoPorts (
			Id INTEGER PRIMARY KEY,
			Proto TEXT,
			Port INTEGER
		);`,
		`CREATE TABLE Details (
			Id INTEGER PRIMARY KEY,
			DetailKey TEXT,
			DetailValue TEXT
		);`,
		`CREATE TABLE ServicesXIP (
			ServiceId INTEGER,
			IPAddressId INTEGER,
			FOREIGN KEY(ServiceId) REFERENCES Services(Id),
			FOREIGN KEY(IPAddressId) REFERENCES IPAddresses(Id)
		);`,
		`CREATE TABLE ServicesXPP (
			ServiceId INTEGER,
			ProtoPortId INTEGER,
			FOREIGN KEY(ServiceId) REFERENCES Services(Id),
			FOREIGN KEY(ProtoPortId) REFERENCES ProtoPorts(Id)
		);`,
		`CREATE TABLE ServicesXDetails (
			ServiceId INTEGER,
			DetailId INTEGER,
			FOREIGN KEY(ServiceId) REFERENCES Services(Id),
			FOREIGN KEY(DetailId) REFERENCES Details(Id)
		);`,
	}

	for _, stmt := range tableStatements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// registerService registers a new service in the database.
func registerService(rsc *UnitAsset, rec *forms.ServiceRecord_v1) error {
	now := time.Now()
	rec.Created = now.Format(time.RFC3339)
	rec.Updated = now.Format(time.RFC3339)
	rec.EndOfValidity = now.Add(time.Duration(rec.RegLife) * time.Second).Format(time.RFC3339)

	rsc.mtx.Lock()
	defer rsc.mtx.Unlock()
	tx, err := rsc.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	result, err := rsc.db.Exec(`
		INSERT INTO Services (
			Definition, SystemName, Certificate, SubPath, Version,
			Created, Updated, RegLife, EndOfValidity, SubscribeAble, ACost, CUnit
		) VALUES (?, ?, ?, ?, ?, datetime('now'), ?, ?, ?, ?, ?, ?)
	`, rec.ServiceDefinition, rec.SystemName, rec.Certificate, rec.SubPath, rec.Version, rec.Updated, rec.RegLife, rec.EndOfValidity, rec.SubscribeAble, rec.ACost, rec.CUnit)
	if err != nil {
		return err
	}

	sRecordId, err := result.LastInsertId()
	if err != nil {
		return err
	}
	rec.Id = int(sRecordId)

	rsc.sched.AddTask(now.Add(time.Duration(rec.RegLife)*time.Second), func() { checkExpiration(rsc, rec.Id) }, rec.Id)

	for _, ipAddress := range rec.IPAddresses {
		result, err := rsc.db.Exec(`INSERT INTO IPAddresses (IPAddress) VALUES (?)`, ipAddress)
		if err != nil {
			return err
		}
		ipAddressId, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = rsc.db.Exec(`INSERT INTO ServicesXIP (ServiceId, IPAddressId) VALUES (?, ?)`, sRecordId, ipAddressId); err != nil {
			return err
		}
	}

	for proto, port := range rec.ProtoPort {
		result, err := rsc.db.Exec(`INSERT INTO ProtoPorts (Proto, Port) VALUES (?, ?)`, proto, port)
		if err != nil {
			return err
		}
		protoPortId, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = rsc.db.Exec(`INSERT INTO ServicesXPP (ServiceId, ProtoPortId) VALUES (?, ?)`, sRecordId, protoPortId); err != nil {
			return err
		}
	}

	for key, values := range rec.Details {
		for _, value := range values {
			result, err := rsc.db.Exec(`INSERT INTO Details (DetailKey, DetailValue) VALUES (?, ?)`, key, value)
			if err != nil {
				return err
			}
			detailId, err := result.LastInsertId()
			if err != nil {
				return err
			}
			if _, err = rsc.db.Exec(`INSERT INTO ServicesXDetails (ServiceId, DetailId) VALUES (?, ?)`, sRecordId, detailId); err != nil {
				return err
			}
		}
	}
	return nil
}

// extendServiceValidity extends the validity of an existing service record.
func extendServiceValidity(rsc *UnitAsset, rec *forms.ServiceRecord_v1) error {
	rsc.mtx.Lock()
	defer rsc.mtx.Unlock()
	tx, err := rsc.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	var regLife int
	var systemName, created string
	if err = rsc.db.QueryRow(`SELECT RegLife, SystemName, Created FROM Services WHERE Id = ?`, rec.Id).Scan(&regLife, &systemName, &created); err != nil {
		return err
	}

	recCreated, err := time.Parse(time.RFC3339, rec.Created)
	if err != nil {
		return err
	}
	dbCreated, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return err
	}
	if !recCreated.Equal(dbCreated) {
		return errors.New("mismatch between received record and database record")
	}

	now := time.Now()
	expirationTime := now.Add(time.Duration(regLife) * time.Second).Format(time.RFC3339)
	stmt, err := rsc.db.Prepare(`UPDATE Services SET Updated = datetime('now'), EndOfValidity = ? WHERE Id = ?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	if _, err = stmt.Exec(expirationTime, rec.Id); err != nil {
		return err
	}

	rec.RegLife = regLife
	rec.EndOfValidity = expirationTime
	rsc.sched.AddTask(now.Add(time.Duration(rec.RegLife)*time.Second), func() { checkExpiration(rsc, rec.Id) }, rec.Id)
	return nil
}

// listCurrentServices lists all current services in the registry.
func listCurrentServices(rsc *UnitAsset) []string {
	allServices, err := getAllRecords(rsc)
	if err != nil {
		fmt.Println("Error in querying all services")
	}
	sList := make([]string, 0)
	for _, serRec := range allServices {
		metaservice := ""
		for key, values := range serRec.Details {
			metaservice += key + ": " + fmt.Sprintf("%v", values) + " "
		}
		hyperlink := "http://" + serRec.IPAddresses[0] + ":" + strconv.Itoa(int(serRec.ProtoPort["http"])) + "/" + serRec.SystemName + "/" + serRec.SubPath
		parts := strings.Split(serRec.SubPath, "/")
		uaName := parts[0]
		sLine := "<p>Service ID: " + strconv.Itoa(int(serRec.Id)) + " with definition <b><a href=\"" + hyperlink + "\">" + serRec.ServiceDefinition + "</b></a> from the <b>" + serRec.SystemName + "/" + uaName + "</b> with details " + metaservice + " will expire at: " + serRec.EndOfValidity + "</p>"
		sList = append(sList, sLine)
	}
	return sList
}

// getAllRecords retrieves all service records from the database.
func getAllRecords(rsc *UnitAsset) ([]forms.ServiceRecord_v1, error) {
	var records []forms.ServiceRecord_v1

	rsc.mtx.RLock()
	defer rsc.mtx.RUnlock()
	rows, err := rsc.db.Query(`
		SELECT Id, Definition, SystemName, Certificate, SubPath, Version, Created, Updated, RegLife, EndOfValidity, SubscribeAble, ACost, CUnit
		FROM Services
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var rec forms.ServiceRecord_v1
		if err := rows.Scan(&rec.Id, &rec.ServiceDefinition, &rec.SystemName, &rec.Certificate, &rec.SubPath, &rec.Version, &rec.Created, &rec.Updated, &rec.RegLife, &rec.EndOfValidity, &rec.SubscribeAble, &rec.ACost, &rec.CUnit); err != nil {
			return nil, err
		}

		if rec.IPAddresses, err = getIPAddresses(rsc, rec.Id); err != nil {
			return nil, err
		}
		if rec.ProtoPort, err = getProtoPorts(rsc, rec.Id); err != nil {
			return nil, err
		}
		if rec.Details, err = getDetails(rsc, rec.Id); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

// getRecord retrieves a specific service record by its ID.
func getRecord(rsc *UnitAsset, id int) (*forms.ServiceRecord_v1, error) {
	var err error
	rec := &forms.ServiceRecord_v1{}
	rsc.mtx.RLock()
	defer rsc.mtx.RUnlock()
	row := rsc.db.QueryRow(`
		SELECT Definition, SystemName, Certificate, SubPath, Version, Created, Updated, RegLife, EndOfValidity, SubscribeAble, ACost, CUnit
		FROM Services WHERE Id = ?
	`, id)
	if err := row.Scan(&rec.ServiceDefinition, &rec.SystemName, &rec.Certificate, &rec.SubPath, &rec.Version, &rec.Created, &rec.Updated, &rec.RegLife, &rec.EndOfValidity, &rec.SubscribeAble, &rec.ACost, &rec.CUnit); err != nil {
		return nil, err
	}
	rec.Id = id

	if rec.IPAddresses, err = getIPAddresses(rsc, id); err != nil {
		return nil, err
	}
	if rec.ProtoPort, err = getProtoPorts(rsc, id); err != nil {
		return nil, err
	}
	if rec.Details, err = getDetails(rsc, id); err != nil {
		return nil, err
	}

	return rec, nil
}

// getIPAddresses retrieves IP addresses linked to a service.
func getIPAddresses(rsc *UnitAsset, serviceId int) ([]string, error) {
	var ips []string
	rows, err := rsc.db.Query(`
		 SELECT IPAddress FROM IPAddresses 
		 INNER JOIN ServicesXIP ON IPAddresses.Id = ServicesXIP.IPAddressId
		 WHERE ServicesXIP.ServiceId = ?
	 `, serviceId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

// getProtoPorts retrieves protocol-port pairs linked to a service.
func getProtoPorts(rsc *UnitAsset, serviceId int) (map[string]int, error) {
	protoPorts := make(map[string]int)
	rows, err := rsc.db.Query(`
		 SELECT Proto, Port FROM ProtoPorts 
		 INNER JOIN ServicesXPP ON ProtoPorts.Id = ServicesXPP.ProtoPortId
		 WHERE ServicesXPP.ServiceId = ?
	 `, serviceId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var proto string
		var port int
		if err := rows.Scan(&proto, &port); err != nil {
			return nil, err
		}
		protoPorts[proto] = port
	}
	return protoPorts, rows.Err()
}

// getDetails retrieves details linked to a service.
func getDetails(rsc *UnitAsset, serviceId int) (map[string][]string, error) {
	details := make(map[string][]string)
	rows, err := rsc.db.Query(`
		 SELECT DetailKey, DetailValue FROM Details 
		 INNER JOIN ServicesXDetails ON Details.Id = ServicesXDetails.DetailId
		 WHERE ServicesXDetails.ServiceId = ?
	 `, serviceId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		details[key] = append(details[key], value)
	}
	return details, rows.Err()
}

// checkExpiration checks if a service has expired and deletes it if it has.
func checkExpiration(rsc *UnitAsset, servId int) {
	var expiration time.Time
	rsc.mtx.RLock()
	err := rsc.db.QueryRow(`SELECT EndOfValidity FROM Services WHERE Id = ?`, servId).Scan(&expiration)
	rsc.mtx.RUnlock()
	if err != nil {
		log.Printf("The service record with id %d is already deleted, %s\n", servId, err)
		return
	}
	if time.Now().After(expiration) {
		deleteCompleteServiceById(rsc, servId)
	}
}

// deleteCompleteServiceById deletes a service record and all related information.
func deleteCompleteServiceById(rsc *UnitAsset, serviceId int) error {
	rsc.mtx.Lock()
	defer rsc.mtx.Unlock()
	tx, err := rsc.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec("DELETE FROM ServicesXIP WHERE ServiceId = ?", serviceId); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM ServicesXPP WHERE ServiceId = ?", serviceId); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM ServicesXDetails WHERE ServiceId = ?", serviceId); err != nil {
		return err
	}
	if _, err = tx.Exec(`
		 DELETE FROM IPAddresses
		 WHERE Id NOT IN (SELECT IPAddressId FROM ServicesXIP)
	 `); err != nil {
		return err
	}
	if _, err = tx.Exec(`
		 DELETE FROM ProtoPorts
		 WHERE Id NOT IN (SELECT ProtoPortId FROM ServicesXPP)
	 `); err != nil {
		return err
	}
	if _, err = tx.Exec(`
		 DELETE FROM Details
		 WHERE Id NOT IN (SELECT DetailId FROM ServicesXDetails)
	 `); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM Services WHERE Id = ?", serviceId); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return err
	}

	fmt.Printf("Complete service record with id %d and its related data has been deleted\n", serviceId)
	return nil
}

// findServices finds services based on the provided service description.
func findServices(rsc *UnitAsset, serviceDescription forms.ServiceQuest_v1) ([]forms.ServiceRecord_v1, error) {
	query := `
		SELECT Id FROM Services
		WHERE Definition = ?`
	params := []interface{}{serviceDescription.ServiceDefinition}

	// If there are details to filter by, we add conditions for them
	if len(serviceDescription.Details) > 0 {
		query += " AND Id IN (SELECT ServiceId FROM ServicesXDetails WHERE DetailId IN (SELECT Id FROM Details WHERE"
		first := true
		for key, values := range serviceDescription.Details {
			for _, value := range values {
				if !first {
					query += " OR "
				}
				query += " (DetailKey = ? AND DetailValue = ?)"
				params = append(params, key, value)
				first = false
			}
		}
		query += "))"
	}

	// Debugging: Print the query and params to verify them
	// fmt.Printf("Query: %s\nParams: %v\n", query, params)

	rsc.mtx.RLock()
	defer rsc.mtx.RUnlock()

	rows, err := rsc.db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var serviceRecords []forms.ServiceRecord_v1
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		record, err := getRecord(rsc, id)
		if err != nil {
			return nil, err
		}
		serviceRecords = append(serviceRecords, *record)
	}

	return serviceRecords, rows.Err()
}

// getUniqueSystems populates the list of systems in a local cloud
func getUniqueSystems(rsc *UnitAsset) (*forms.SystemRecordList_v1, error) {
	uniqueSystems := make(map[string]forms.SystemRecord_v1)

	rsc.mtx.RLock()
	defer rsc.mtx.RUnlock()
	rows, err := rsc.db.Query(`
		SELECT s.SystemName, ip.IPAddress, pp.Port
		FROM Services s
		INNER JOIN ServicesXIP sip ON s.Id = sip.ServiceId
		INNER JOIN IPAddresses ip ON sip.IPAddressId = ip.Id
		INNER JOIN ServicesXPP spp ON s.Id = spp.ServiceId
		INNER JOIN ProtoPorts pp ON spp.ProtoPortId = pp.Id
		WHERE pp.Proto = 'http'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var systemName, ipAddress string
		var port int
		if err := rows.Scan(&systemName, &ipAddress, &port); err != nil {
			return nil, err
		}

		if sysRec, exists := uniqueSystems[systemName]; exists {
			// Check for duplicate IP addresses
			ipExists := false
			for _, existingIP := range sysRec.IPAddresses {
				if existingIP == ipAddress {
					ipExists = true
					break
				}
			}
			if !ipExists {
				sysRec.IPAddresses = append(sysRec.IPAddresses, ipAddress)
			}
			uniqueSystems[systemName] = sysRec
		} else {
			uniqueSystems[systemName] = forms.SystemRecord_v1{
				SystemName:  systemName,
				IPAddresses: []string{ipAddress},
				Port:        port,
				Version:     "SystemRecord_v1",
			}
		}
	}

	systemList := make([]forms.SystemRecord_v1, 0, len(uniqueSystems))
	for _, sysRec := range uniqueSystems {
		systemList = append(systemList, sysRec)
	}

	return &forms.SystemRecordList_v1{
		List:    systemList,
		Version: "SystemRecordList_v1",
	}, nil
}
