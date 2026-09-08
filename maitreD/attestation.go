/*******************************************************************************
 * Copyright (c) 2026 Synecdoque
 *
 * The software is licensed under the MIT License. See the LICENSE file in this
 * repository for details.
 *
 * Contributors:
 *   Jan A. van Deventer, Luleå - initial implementation
 ***************************************************************************SDG*/

package main

// The attestation statement: what this maitreD measured, signed so the asker
// can tell it came from a maitreD.
//
// The maitreD no longer holds a whitelist. It measures — only it can map a pid
// to the file that process is running and hash it — and the CA, which owns
// whitelist.json, decides. The policy never leaves the machine that owns it, so
// there is nothing on this host to tamper with and nothing to keep in sync.
//
// The signature closes the other half. The CA reaches this endpoint over plain
// HTTP at an address the requesting system controls, and it must be exempt from
// authorization because the bootstrap plane is what makes tokens possible. So
// the transport proves nothing, and without a signature anything that can
// listen on this port could answer "approved" for itself. A statement signed
// with the key behind this maitreD's own certificate is something the CA can
// check against the certificate it issued.

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"
)

// attestationRequest is what the CA asks for. The nonce is the CA's, and it is
// what makes a reply fresh: a recorded answer cannot be replayed against a
// challenge that has not been issued before.
type attestationRequest struct {
	PID   int    `json:"pid"`
	Nonce string `json:"nonce"`
}

// attestationStatement is what this maitreD answers with. It is a measurement
// and not a verdict — there is no "approved" field, because approving is not
// this system's job.
type attestationStatement struct {
	PID       int    `json:"pid"`
	Hash      string `json:"hash"`
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`

	Signature   string `json:"signature"`   // base64, ASN.1 ECDSA over signedMessage
	Certificate string `json:"certificate"` // PEM, as issued by the CA
}

// attestationContext is the domain separator. It is part of the signed bytes so
// a signature made here can never be mistaken for a signature over anything
// else this key signs.
const attestationContext = "mbaigo-attestation-v1"

// signedMessage is the exact byte string both sides sign and verify.
//
// Every field is present and the order is fixed. A signature over the hash
// alone would be replayable against a different pid, and one that omitted the
// nonce would be replayable at any time.
func signedMessage(pid int, hash, nonce, timestamp string) []byte {
	return []byte(attestationContext + "\n" +
		nonce + "\n" +
		strconv.Itoa(pid) + "\n" +
		hash + "\n" +
		timestamp)
}

// sign produces the statement for one measurement.
func sign(key *ecdsa.PrivateKey, certPEM string, pid int, hash, nonce string) (attestationStatement, error) {
	if key == nil || certPEM == "" {
		return attestationStatement{}, fmt.Errorf("this maitreD has not enrolled yet and cannot sign")
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256(signedMessage(pid, hash, nonce, ts))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return attestationStatement{}, fmt.Errorf("signing the attestation: %w", err)
	}
	return attestationStatement{
		PID:         pid,
		Hash:        hash,
		Nonce:       nonce,
		Timestamp:   ts,
		Signature:   base64.StdEncoding.EncodeToString(sig),
		Certificate: certPEM,
	}, nil
}
