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
 ***************************************************************************SDG*/

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// newTestSystem builds a minimal System for use in tests.
func newTestSystem() *components.System {
	ctx := context.Background()
	sys := components.NewSystem("ca", ctx)
	sys.Husk = &components.Husk{
		Description: "test ca",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		Host:        components.NewDevice(),
		ProtoPort:   map[string]int{"http": 20100},
	}
	return &sys
}

// makeTestCA generates a self-signed CA certificate and private key.
func makeTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert, privateKey
}

// writeCAFiles saves PEM-encoded cert and key to the named files inside dir.
func writeCAFiles(t *testing.T, dir, certFilename, keyFilename string, cert *x509.Certificate, key *ecdsa.PrivateKey) {
	t.Helper()
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	if err := os.WriteFile(filepath.Join(dir, certFilename), certPEM, 0644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	if err := os.WriteFile(filepath.Join(dir, keyFilename), keyPEM, 0644); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// makeCSRPEM generates and returns a PEM-encoded certificate signing request.
func makeCSRPEM(t *testing.T) []byte {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key for CSR: %v", err)
	}
	template := x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "test-client"},
	}
	csrBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, privateKey)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrBytes})
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "certification" {
		t.Errorf("name = %q, want %q", ua.GetName(), "certification")
	}
	services := ua.GetServices()
	svc, ok := services["certify"]
	if !ok {
		t.Fatal("expected 'certify' entry in ServicesMap")
	}
	if svc.Definition != "certify" {
		t.Errorf("service definition = %q, want %q", svc.Definition, "certify")
	}
	if ua.GetTraits() == nil {
		t.Error("Traits should be non-nil")
	}
}

// ── Traits serialisation ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	original := Traits{SafeSWare: true}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var decoded Traits
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if decoded.SafeSWare != original.SafeSWare {
		t.Errorf("SafeSWare = %v, want %v", decoded.SafeSWare, original.SafeSWare)
	}
	// Private fields must not leak into JSON; public fields must round-trip.
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if _, ok := raw["privateKey"]; ok {
		t.Error("privateKey must not be exported to JSON")
	}
	if _, ok := raw["certificate"]; ok {
		t.Error("certificate must not be exported to JSON")
	}
	if v, ok := raw["safeSWare"].(bool); !ok || !v {
		t.Error("safeSWare must be true in JSON")
	}
}

// ── signCSR ───────────────────────────────────────────────────────────────────

func TestSignCSR(t *testing.T) {
	caCert, caKey := makeTestCA(t)
	csrPEM := makeCSRPEM(t)

	block, _ := pem.Decode(csrPEM)
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}

	t.Run("valid CSR produces a verifiable certificate", func(t *testing.T) {
		certPEM, err := signCSR(csr, caCert, caKey)
		if err != nil {
			t.Fatalf("signCSR: %v", err)
		}
		blk, _ := pem.Decode(certPEM)
		if blk == nil || blk.Type != "CERTIFICATE" {
			t.Fatal("output is not a valid CERTIFICATE PEM block")
		}
		cert, err := x509.ParseCertificate(blk.Bytes)
		if err != nil {
			t.Fatalf("parse signed cert: %v", err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(caCert)
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
			t.Errorf("certificate does not verify against CA: %v", err)
		}
		if cert.IsCA {
			t.Error("signed certificate should not be a CA certificate")
		}
	})

	t.Run("nil CA private key returns error", func(t *testing.T) {
		_, err := signCSR(csr, caCert, nil)
		if err == nil {
			t.Error("expected error when CA private key is nil")
		}
	})
}

// ── generateSelfSignedCert ────────────────────────────────────────────────────

func TestGenerateSelfSignedCert(t *testing.T) {
	sys := newTestSystem()

	certPEM, keyPEM, err := generateSelfSignedCert(sys)
	if err != nil {
		t.Fatalf("generateSelfSignedCert: %v", err)
	}

	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		t.Fatal("certPEM is not a valid CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("expected IsCA = true for self-signed CA cert")
	}
	if cert.NotAfter.Before(time.Now().Add(364 * 24 * time.Hour)) {
		t.Error("certificate validity is shorter than expected")
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		t.Fatal("keyPEM is not a valid EC PRIVATE KEY block")
	}
	if _, err := x509.ParseECPrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("parse EC private key: %v", err)
	}
}

// ── loadCACertificate ─────────────────────────────────────────────────────────

func TestLoadCACertificate(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := makeTestCA(t)
	writeCAFiles(t, dir, "ca_cert.pem", "ca_key.pem", caCert, caKey)
	certFile := filepath.Join(dir, "ca_cert.pem")
	keyFile := filepath.Join(dir, "ca_key.pem")

	t.Run("loads valid files successfully", func(t *testing.T) {
		cert, key, err := loadCACertificate(certFile, keyFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cert.SerialNumber.Cmp(caCert.SerialNumber) != 0 {
			t.Error("loaded certificate serial number does not match original")
		}
		if key == nil {
			t.Error("expected non-nil private key")
		}
	})

	t.Run("missing cert file returns error", func(t *testing.T) {
		_, _, err := loadCACertificate(filepath.Join(dir, "no_such.pem"), keyFile)
		if err == nil {
			t.Error("expected error for missing cert file")
		}
	})

	t.Run("missing key file returns error", func(t *testing.T) {
		_, _, err := loadCACertificate(certFile, filepath.Join(dir, "no_such.pem"))
		if err == nil {
			t.Error("expected error for missing key file")
		}
	})

	t.Run("invalid cert PEM returns error", func(t *testing.T) {
		badCert := filepath.Join(dir, "bad_cert.pem")
		os.WriteFile(badCert, []byte("this is not PEM"), 0644)
		_, _, err := loadCACertificate(badCert, keyFile)
		if err == nil {
			t.Error("expected error for invalid cert PEM")
		}
	})

	t.Run("invalid key PEM returns error", func(t *testing.T) {
		badKey := filepath.Join(dir, "bad_key.pem")
		os.WriteFile(badKey, []byte("this is not PEM"), 0644)
		_, _, err := loadCACertificate(certFile, badKey)
		if err == nil {
			t.Error("expected error for invalid key PEM")
		}
	})
}

// ── ensureCertificate ─────────────────────────────────────────────────────────

func TestEnsureCertificate(t *testing.T) {
	sys := newTestSystem()

	t.Run("generates files when none exist", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		cert, key, err := ensureCertificate(sys, "ca_certificate.pem", "ca_private_key.pem")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cert == nil || key == nil {
			t.Error("expected non-nil cert and key")
		}
		if _, err := os.Stat("ca_certificate.pem"); err != nil {
			t.Error("cert file was not created")
		}
		if _, err := os.Stat("ca_private_key.pem"); err != nil {
			t.Error("key file was not created")
		}
	})

	t.Run("loads existing files without overwriting", func(t *testing.T) {
		dir := t.TempDir()
		existing, existingKey := makeTestCA(t)
		writeCAFiles(t, dir, "ca_certificate.pem", "ca_private_key.pem", existing, existingKey)
		t.Chdir(dir)
		cert, _, err := ensureCertificate(sys, "ca_certificate.pem", "ca_private_key.pem")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cert.SerialNumber.Cmp(existing.SerialNumber) != 0 {
			t.Error("loaded a different certificate than the one that was present")
		}
	})
}

// ── newResource ───────────────────────────────────────────────────────────────

func TestNewResource(t *testing.T) {
	t.Run("creates unit asset with correct fields", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		sys := newTestSystem()
		certifySvc := components.Service{
			Definition:  "certify",
			SubPath:     "certify",
			Details:     map[string][]string{"Forms": {"csr.pem"}},
			RegPeriod:   30,
			Description: "signs CSRs",
		}
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "certification",
			Mission:  "sign_csrs",
			Details:  map[string][]string{"PKI": {"X.509"}},
			Services: []components.Service{certifySvc},
		}

		ua, cleanup := newResource(cfgAsset, sys)
		defer cleanup()

		if ua.GetName() != "certification" {
			t.Errorf("name = %q, want %q", ua.GetName(), "certification")
		}
		if ua.Mission != "sign_csrs" {
			t.Errorf("mission = %q, want %q", ua.Mission, "sign_csrs")
		}
		if ua.ServingFunc == nil {
			t.Error("ServingFunc must be set")
		}
		if _, ok := ua.GetServices()["certify"]; !ok {
			t.Error("expected 'certify' service in map")
		}
		if sys.Husk.Certificate == "" {
			t.Error("sys.Husk.Certificate was not populated")
		}
	})

	t.Run("unmarshals SafeSWare trait from config", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)

		sys := newTestSystem()
		traitJSON, _ := json.Marshal(Traits{SafeSWare: true})
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "certification",
			Traits:   []json.RawMessage{traitJSON},
			Services: []components.Service{{Definition: "certify", SubPath: "certify"}},
		}

		ua, cleanup := newResource(cfgAsset, sys)
		defer cleanup()

		traits, ok := ua.GetTraits().(*Traits)
		if !ok {
			t.Fatal("traits are not of type *Traits")
		}
		if !traits.SafeSWare {
			t.Error("SafeSWare should be true after unmarshaling")
		}
	})
}

// ── serving ───────────────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	caCert, caKey := makeTestCA(t)
	traits := &Traits{certificate: caCert, privateKey: caKey}

	t.Run("certify path dispatches correctly", func(t *testing.T) {
		csrPEM := makeCSRPEM(t)
		req := httptest.NewRequest(http.MethodPost, "/ca/certification/certify", bytes.NewReader(csrPEM))
		w := httptest.NewRecorder()
		serving(traits, w, req, "certify")
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown path returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/ca/certification/unknown", nil)
		w := httptest.NewRecorder()
		serving(traits, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// ── certifying ────────────────────────────────────────────────────────────────

func TestCertifying(t *testing.T) {
	caCert, caKey := makeTestCA(t)
	traits := &Traits{certificate: caCert, privateKey: caKey}

	t.Run("POST with valid CSR returns signed certificate", func(t *testing.T) {
		csrPEM := makeCSRPEM(t)
		req := httptest.NewRequest(http.MethodPost, "/certify", bytes.NewReader(csrPEM))
		w := httptest.NewRecorder()
		traits.certifying(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		block, _ := pem.Decode(w.Body.Bytes())
		if block == nil || block.Type != "CERTIFICATE" {
			t.Error("response body is not a valid CERTIFICATE PEM block")
		}
		// The signed certificate should verify against the test CA.
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse returned cert: %v", err)
		}
		pool := x509.NewCertPool()
		pool.AddCert(caCert)
		if _, err := cert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
			t.Errorf("returned certificate does not verify: %v", err)
		}
	})

	t.Run("GET returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/certify", nil)
		w := httptest.NewRecorder()
		traits.certifying(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})

	t.Run("POST with non-PEM body returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/certify", bytes.NewReader([]byte("not a pem")))
		w := httptest.NewRecorder()
		traits.certifying(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("POST with wrong PEM type returns 400", func(t *testing.T) {
		wrongPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a csr")})
		req := httptest.NewRequest(http.MethodPost, "/certify", bytes.NewReader(wrongPEM))
		w := httptest.NewRecorder()
		traits.certifying(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("POST with invalid ASN.1 inside CSR block returns 400", func(t *testing.T) {
		garbledPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte("garbage")})
		req := httptest.NewRequest(http.MethodPost, "/certify", bytes.NewReader(garbledPEM))
		w := httptest.NewRecorder()
		traits.certifying(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}
