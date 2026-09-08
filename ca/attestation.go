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

// Asking a maitreD what is running, and deciding whether to believe it.
//
// The whitelist stays here, on the machine that owns it. Previously the CA
// served its whitelist to every maitreD and then asked each maitreD to apply
// the CA's own policy — a round trip that put a copy of the policy on every
// host, gave each copy a five-minute staleness, and left a file on disk that
// granted a certificate to anything whose hash was added to it.
//
// Now the maitreD measures and the CA decides. A whitelist edit takes effect on
// the next certificate request, because this is the process that reads the file.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// The wire contract, defined here as well as in the maitreD because the two are
// separate `package main` binaries with no shared package. The JSON on the wire
// is the source of truth.
type attestationRequest struct {
	PID   int    `json:"pid"`
	Nonce string `json:"nonce"`
}

type attestationStatement struct {
	PID       int    `json:"pid"`
	Hash      string `json:"hash"`
	Nonce     string `json:"nonce"`
	Timestamp string `json:"timestamp"`

	Signature   string `json:"signature"`
	Certificate string `json:"certificate"`
}

const (
	attestationContext = "mbaigo-attestation-v1"

	// maitreDCommonName is the only identity whose word is taken on what is
	// running on a host.
	maitreDCommonName = "maitreD"

	// attestationSkew is how far a statement's own timestamp may sit from the
	// CA's clock. The nonce is what actually makes a reply fresh; this only
	// catches a maitreD whose clock is wrong enough to be worth knowing about.
	attestationSkew = 5 * time.Minute
)

func signedMessage(pid int, hash, nonce, timestamp string) []byte {
	return []byte(attestationContext + "\n" +
		nonce + "\n" +
		strconv.Itoa(pid) + "\n" +
		hash + "\n" +
		timestamp)
}

// newNonce returns a fresh challenge. Without one, a statement recorded from a
// legitimate attestation could be replayed for ever.
func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// verifyStatement checks that this statement was made by a maitreD this CA
// certified, about the process that was asked about, in answer to this
// challenge. It returns the measured hash only if all of that holds.
//
// caCert is this CA's own certificate, which is what a genuine maitreD's
// certificate must chain to.
func verifyStatement(st attestationStatement, caCert *x509.Certificate, pid int, nonce string) (string, error) {
	// Bind the answer to the question first. Everything below is expensive and
	// none of it matters if the statement is about something else.
	if st.PID != pid {
		return "", fmt.Errorf("statement is about pid %d, not %d", st.PID, pid)
	}
	if st.Nonce != nonce {
		return "", fmt.Errorf("statement answers a different challenge")
	}
	if st.Hash == "" {
		return "", fmt.Errorf("statement carries no hash")
	}

	block, _ := pem.Decode([]byte(st.Certificate))
	if block == nil {
		return "", fmt.Errorf("statement carries no usable certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing the maitreD certificate: %w", err)
	}

	// Issued by this CA. Without this the whole exercise is theatre: anything
	// that can listen on the maitreD's port could otherwise present a
	// self-signed certificate and vouch for itself.
	if caCert == nil {
		return "", fmt.Errorf("this CA has no certificate of its own to check against")
	}
	if err := cert.CheckSignatureFrom(caCert); err != nil {
		return "", fmt.Errorf("the maitreD certificate was not issued by this CA: %w", err)
	}
	if cert.Subject.CommonName != maitreDCommonName {
		return "", fmt.Errorf("attestation signed by %q, which is not a maitreD", cert.Subject.CommonName)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		return "", fmt.Errorf("the maitreD certificate is not currently valid")
	}

	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return "", fmt.Errorf("the maitreD certificate does not carry an ECDSA key")
	}
	sig, err := base64.StdEncoding.DecodeString(st.Signature)
	if err != nil {
		return "", fmt.Errorf("decoding the signature: %w", err)
	}
	digest := sha256.Sum256(signedMessage(st.PID, st.Hash, st.Nonce, st.Timestamp))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return "", fmt.Errorf("the signature does not match the statement")
	}

	// Last, because a wrong clock is worth reporting but is not an attack on
	// its own: the nonce already establishes freshness.
	ts, err := time.Parse(time.RFC3339Nano, st.Timestamp)
	if err != nil {
		return "", fmt.Errorf("unreadable timestamp %q: %w", st.Timestamp, err)
	}
	if d := now.Sub(ts); d > attestationSkew || d < -attestationSkew {
		return "", fmt.Errorf("the maitreD's clock is %s away from this one", d.Round(time.Second))
	}

	return st.Hash, nil
}

// askMaitreD puts the challenge and reads the statement back.
func askMaitreD(client *http.Client, url string, pid int, nonce string) (attestationStatement, error) {
	body, err := json.Marshal(attestationRequest{PID: pid, Nonce: nonce})
	if err != nil {
		return attestationStatement{}, err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return attestationStatement{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return attestationStatement{}, fmt.Errorf("maitreD answered %d: %s",
			resp.StatusCode, bytes.TrimSpace(msg))
	}
	var st attestationStatement
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return attestationStatement{}, fmt.Errorf("unreadable statement: %w", err)
	}
	return st, nil
}

// approved reports whether hash is in the whitelist.
func approved(wl Whitelist, hash string) bool {
	for _, h := range wl.Hashes {
		if h == hash {
			return true
		}
	}
	return false
}
