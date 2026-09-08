package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// issue mints a certificate the way the CA does, so the tests exercise the real
// chain check rather than a stand-in for it.
func issue(t *testing.T, cn string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	parent, signer := tmpl, key
	if caCert != nil {
		parent, signer = caCert, caKey
	} else {
		// A root, shaped the way the CA shapes its own: CheckSignatureFrom
		// refuses a parent that is not marked as one, which is the check doing
		// its job.
		tmpl.IsCA = true
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func newCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	cert, key, _ := issue(t, "ca", nil, nil)
	return cert, key
}

// statementFrom builds a signed statement the way a maitreD does.
func statementFrom(t *testing.T, key *ecdsa.PrivateKey, certPEM string, pid int, hash, nonce string) attestationStatement {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256(signedMessage(pid, hash, nonce, ts))
	sig, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return attestationStatement{
		PID: pid, Hash: hash, Nonce: nonce, Timestamp: ts,
		Signature:   base64.StdEncoding.EncodeToString(sig),
		Certificate: certPEM,
	}
}

func TestVerifyStatementAcceptsAGenuineMaitreD(t *testing.T) {
	caCert, caKey := newCA(t)
	_, mKey, mPEM := issue(t, "maitreD", caCert, caKey)

	st := statementFrom(t, mKey, mPEM, 42, "abc123", "nonce-abc")
	hash, err := verifyStatement(st, caCert, 42, "nonce-abc")
	if err != nil {
		t.Fatalf("a genuine statement was rejected: %v", err)
	}
	if hash != "abc123" {
		t.Errorf("hash = %q, want abc123", hash)
	}
}

// The attack the signature exists to stop: anything can listen on the maitreD's
// port, because the CA reaches it over plain HTTP at an address the requesting
// system controls. A self-signed certificate must not be believed.
func TestVerifyStatementRefusesAForeignCertificate(t *testing.T) {
	caCert, _ := newCA(t)
	_, rogueKey, roguePEM := issue(t, "maitreD", nil, nil) // self-signed

	st := statementFrom(t, rogueKey, roguePEM, 42, "abc123", "nonce-abc")
	if _, err := verifyStatement(st, caCert, 42, "nonce-abc"); err == nil {
		t.Error("a self-signed certificate was accepted; anyone could vouch for themselves")
	}
}

// A certificate this CA issued, but to something that is not a maitreD. Any
// enrolled system has one of these, so the common name has to be checked.
func TestVerifyStatementRefusesANonMaitreD(t *testing.T) {
	caCert, caKey := newCA(t)
	_, key, certPEM := issue(t, "beekeeper", caCert, caKey)

	st := statementFrom(t, key, certPEM, 42, "abc123", "nonce-abc")
	if _, err := verifyStatement(st, caCert, 42, "nonce-abc"); err == nil {
		t.Error("a certificate belonging to another system was accepted as a maitreD's")
	}
}

// A statement recorded from a legitimate attestation must not work a second
// time, which is the whole purpose of the nonce.
func TestVerifyStatementRefusesAReplay(t *testing.T) {
	caCert, caKey := newCA(t)
	_, mKey, mPEM := issue(t, "maitreD", caCert, caKey)

	st := statementFrom(t, mKey, mPEM, 42, "abc123", "yesterday")
	if _, err := verifyStatement(st, caCert, 42, "today"); err == nil {
		t.Error("a statement answering an old challenge was accepted")
	}
}

// The signature covers the pid, so a statement about an approved process cannot
// be presented for a different one.
func TestVerifyStatementRefusesASwappedPID(t *testing.T) {
	caCert, caKey := newCA(t)
	_, mKey, mPEM := issue(t, "maitreD", caCert, caKey)

	st := statementFrom(t, mKey, mPEM, 42, "abc123", "nonce-abc")
	st.PID = 43
	if _, err := verifyStatement(st, caCert, 43, "nonce-abc"); err == nil {
		t.Error("a statement about pid 42 was accepted for pid 43")
	}
}

// The one that matters most: the hash is what the whitelist is checked against,
// so tampering with it must break the signature.
func TestVerifyStatementRefusesATamperedHash(t *testing.T) {
	caCert, caKey := newCA(t)
	_, mKey, mPEM := issue(t, "maitreD", caCert, caKey)

	st := statementFrom(t, mKey, mPEM, 42, "abc123", "nonce-abc")
	st.Hash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := verifyStatement(st, caCert, 42, "nonce-abc"); err == nil {
		t.Error("the hash was changed and the statement still verified")
	}
}

func TestNoncesDiffer(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := newNonce()
		if err != nil {
			t.Fatalf("newNonce: %v", err)
		}
		if len(n) < 32 {
			t.Fatalf("nonce %q is shorter than the maitreD will accept", n)
		}
		if seen[n] {
			t.Fatal("newNonce repeated itself")
		}
		seen[n] = true
	}
}

// The whole path: the CA challenges a stand-in maitreD, verifies what comes
// back, and decides against its own whitelist.
func TestRequestAttestationEndToEnd(t *testing.T) {
	caCert, caKey := newCA(t)
	_, mKey, mPEM := issue(t, "maitreD", caCert, caKey)

	const goodHash = "d0a1b2c3"
	var served string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req attestationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if len(req.Nonce) < 32 {
			t.Errorf("the CA sent a %d-character nonce", len(req.Nonce))
		}
		st := statementFrom(t, mKey, mPEM, req.PID, served, req.Nonce)
		json.NewEncoder(w).Encode(st)
	}))
	defer srv.Close()

	dir := t.TempDir()
	wlPath := dir + "/whitelist.json"
	writeJSON(t, wlPath, []string{goodHash})

	tr := &Traits{certificate: caCert, WhitelistPath: wlPath}

	// The CA's own request path, minus the address arithmetic: point it at the
	// stand-in maitreD.
	check := func(pid int) error {
		wl, err := loadWhitelist(tr.WhitelistPath)
		if err != nil {
			return err
		}
		nonce, err := newNonce()
		if err != nil {
			return err
		}
		st, err := askMaitreD(srv.Client(), srv.URL, pid, nonce)
		if err != nil {
			return err
		}
		hash, err := verifyStatement(st, tr.certificate, pid, nonce)
		if err != nil {
			return err
		}
		if !approved(wl, hash) {
			return errNotApproved
		}
		return nil
	}

	served = goodHash
	if err := check(101); err != nil {
		t.Errorf("a whitelisted binary was refused: %v", err)
	}

	served = "not-in-the-list"
	if err := check(101); err == nil {
		t.Error("a binary that is not in the whitelist was approved")
	}

	// And the point of moving the decision here: editing the file takes effect
	// on the next request, with nothing to sync and nobody to wait for.
	writeJSON(t, wlPath, []string{goodHash, "not-in-the-list"})
	if err := check(101); err != nil {
		t.Errorf("the whitelist was edited but the change did not take effect: %v", err)
	}
}

var errNotApproved = &notApprovedError{}

type notApprovedError struct{}

func (e *notApprovedError) Error() string { return "hash is not in the whitelist" }

func osWriteFile(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := osWriteFile(path, b); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A CA that has run before still lists the whitelist service in its
// systemconfig.json, because Configure reads the file that exists rather than
// the template. It must not register a service whose handler is gone.
func TestServedServicesDropsTheObsoleteWhitelist(t *testing.T) {
	svcs := servedServices([]components.Service{
		{Definition: "certify", SubPath: "certify"},
		{Definition: "whitelist", SubPath: "whitelist"},
	})
	if _, ok := svcs["whitelist"]; ok {
		t.Error("the obsolete whitelist service survived; it would register with no handler behind it")
	}
	if _, ok := svcs["certify"]; !ok {
		t.Error("certify was dropped")
	}
}
