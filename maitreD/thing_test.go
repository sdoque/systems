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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// writeTempFile creates a file with the given content in a temp dir and returns its path.
func writeTempFile(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "testexe")
	if err := os.WriteFile(path, content, 0755); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

// sha256Hex returns the hex SHA-256 of data.
func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// withResolveExecutable temporarily replaces resolveExecutable for the duration of the test.
func withResolveExecutable(t *testing.T, fn func(int) (string, error)) {
	t.Helper()
	orig := resolveExecutable
	resolveExecutable = fn
	t.Cleanup(func() { resolveExecutable = orig })
}

// ── initTemplate ──────────────────────────────────────────────────────────────

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.GetName() != "maitreD" {
		t.Errorf("name = %q, want %q", ua.GetName(), "maitreD")
	}
	svc, ok := ua.GetServices()["attest"]
	if !ok {
		t.Fatal("expected 'attest' entry in ServicesMap")
	}
	if svc.Definition != "attest" {
		t.Errorf("service definition = %q, want %q", svc.Definition, "attest")
	}
	if ua.GetTraits() == nil {
		t.Error("Traits should be non-nil")
	}
}

// ── Traits serialization ──────────────────────────────────────────────────────

func TestTraitsSerialization(t *testing.T) {
	// All Traits fields are runtime state, not config: marshalling must
	// produce no operator-visible fields. A future schema addition that
	// accidentally exposes one of these will fail this test.
	original := &Traits{name: "maitreD", LoadPeriod: 15}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	for _, field := range []string{"whitelist", "version", "loaded", "owner", "name"} {
		if _, ok := raw[field]; ok {
			t.Errorf("field %q must not appear in JSON", field)
		}
	}
}

// ── newResource ───────────────────────────────────────────────────────────────

func TestNewResource(t *testing.T) {
	t.Run("creates unit asset with correct fields", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sys := components.NewSystem("maitreD", ctx)
		sys.Husk = &components.Husk{
			Host:      components.NewDevice(),
			ProtoPort: map[string]int{"http": 20101},
		}

		attestSvc := components.Service{
			Definition: "attest",
			SubPath:    "attest",
		}
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "maitreD",
			Mission:  components.MissionCore,
			Services: []components.Service{attestSvc},
		}

		ua, cleanup := newResource(cfgAsset, &sys)
		defer cleanup()

		if ua.GetName() != "maitreD" {
			t.Errorf("name = %q, want %q", ua.GetName(), "maitreD")
		}
		// The taxonomy's value, not a sentence about what the system does. The
		// host sentinel is framework infrastructure like the rest of the core.
		if ua.Mission != components.MissionCore {
			t.Errorf("mission = %q, want %q", ua.Mission, components.MissionCore)
		}
		if ua.ServingFunc == nil {
			t.Error("ServingFunc must be set")
		}
		if _, ok := ua.GetServices()["attest"]; !ok {
			t.Error("expected 'attest' service in map")
		}
	})

	t.Run("ignores any 'whitelist' field carried by an older systemconfig", func(t *testing.T) {
		// The whitelist is now CA-mastered. Operator-supplied whitelist entries
		// in systemconfig.json must be silently ignored, not loaded as truth.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sys := components.NewSystem("maitreD", ctx)
		sys.Husk = &components.Husk{
			Host:      components.NewDevice(),
			ProtoPort: map[string]int{"http": 20101},
		}

		// Hand-craft a traits payload using the legacy "whitelist" key.
		traitJSON := json.RawMessage(`{"whitelist":["aabbcc"]}`)
		cfgAsset := usecases.ConfigurableAsset{
			Name:     "maitreD",
			Traits:   []json.RawMessage{traitJSON},
			Services: []components.Service{{Definition: "attest", SubPath: "attest"}},
		}

		ua, cleanup := newResource(cfgAsset, &sys)
		defer cleanup()

		tr, ok := ua.GetTraits().(*Traits)
		if !ok {
			t.Fatal("traits are not of type *Traits")
		}
		if tr.owner == nil {
			t.Error("the asset was built without a system to sign on behalf of")
		}
	})
}

// ── serving ───────────────────────────────────────────────────────────────────

func TestServing(t *testing.T) {
	exeData := []byte("fake-executable")
	exePath := writeTempFile(t, exeData)
	caCert, caKey := testCA(t)
	tr, _ := testTraitsWithCert(t, caCert, caKey)

	withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })

	t.Run("attest path dispatches correctly", func(t *testing.T) {
		body, _ := json.Marshal(attestationRequest{PID: 42, Nonce: "0123456789abcdef0123456789abcdef"})
		req := httptest.NewRequest(http.MethodPost, "/maitreD/maitreD/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		serving(tr, w, req, "attest")
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
	})

	t.Run("unknown path returns 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
		w := httptest.NewRecorder()
		serving(tr, w, req, "unknown")
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})
}

// ── attest ────────────────────────────────────────────────────────────────────

func TestAttest(t *testing.T) {
	exeData := []byte("some-binary-content")
	exePath := writeTempFile(t, exeData)
	wantHash := sha256Hex(exeData)

	caCert, caKey := testCA(t)
	tr, mKey := testTraitsWithCert(t, caCert, caKey)

	const nonce = "0123456789abcdef0123456789abcdef"

	post := func(t *testing.T, tr *Traits, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		tr.attest(w, req)
		return w
	}

	t.Run("returns a signed measurement, not a verdict", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })

		body, _ := json.Marshal(attestationRequest{PID: 99, Nonce: nonce})
		w := post(t, tr, body)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
		}
		var st attestationStatement
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if st.Hash != wantHash {
			t.Errorf("hash = %s, want %s", st.Hash, wantHash)
		}
		if st.Nonce != nonce || st.PID != 99 {
			t.Errorf("statement does not answer the question asked: pid=%d nonce=%q", st.PID, st.Nonce)
		}
		if st.Signature == "" || st.Certificate == "" {
			t.Error("statement is unsigned; the CA would have no way to tell it from anything else on the port")
		}
		verifyWithKey(t, st, &mKey.PublicKey)
	})

	// The maitreD no longer knows what is allowed, so an unrecognized binary is
	// not its business: it measures and answers 200. The CA refuses.
	t.Run("an unknown binary is still measured", func(t *testing.T) {
		otherPath := writeTempFile(t, []byte("untrusted-binary"))
		withResolveExecutable(t, func(pid int) (string, error) { return otherPath, nil })

		body, _ := json.Marshal(attestationRequest{PID: 99, Nonce: nonce})
		if w := post(t, tr, body); w.Code != http.StatusOK {
			t.Errorf("status = %d, want 200: judging is the CA's job now", w.Code)
		}
	})

	t.Run("unresolvable PID returns 500", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) {
			return "", fmt.Errorf("no such process")
		})
		body, _ := json.Marshal(attestationRequest{PID: 99, Nonce: nonce})
		if w := post(t, tr, body); w.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", w.Code)
		}
	})

	t.Run("non-POST returns 405", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/attest", nil)
		w := httptest.NewRecorder()
		tr.attest(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", w.Code)
		}
	})

	t.Run("invalid JSON body returns 400", func(t *testing.T) {
		if w := post(t, tr, []byte("not json")); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("zero PID returns 400", func(t *testing.T) {
		body, _ := json.Marshal(attestationRequest{PID: 0, Nonce: nonce})
		if w := post(t, tr, body); w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	// A statement signed against no challenge, or a guessable one, could be
	// recorded and replayed. There is no unsigned or un-nonced mode.
	t.Run("a missing or short nonce returns 400", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })
		for _, n := range []string{"", "short"} {
			body, _ := json.Marshal(attestationRequest{PID: 99, Nonce: n})
			if w := post(t, tr, body); w.Code != http.StatusBadRequest {
				t.Errorf("nonce %q gave %d, want 400", n, w.Code)
			}
		}
	})

	// Before enrolment there is no key, and an unsigned answer is worth less
	// than none: the CA could not tell this maitreD from anything else.
	t.Run("returns 503 before this maitreD has enrolled", func(t *testing.T) {
		withResolveExecutable(t, func(pid int) (string, error) { return exePath, nil })
		notReady := &Traits{owner: &components.System{Husk: &components.Husk{}}}
		body, _ := json.Marshal(attestationRequest{PID: 99, Nonce: nonce})
		if w := post(t, notReady, body); w.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", w.Code)
		}
	})
}

// ── hashFile ──────────────────────────────────────────────────────────────────

func TestHashFile(t *testing.T) {
	content := []byte("hello maitreD")
	path := writeTempFile(t, content)

	t.Run("produces correct SHA-256", func(t *testing.T) {
		got, err := hashFile(path)
		if err != nil {
			t.Fatalf("hashFile: %v", err)
		}
		if want := sha256Hex(content); got != want {
			t.Errorf("hash = %s, want %s", got, want)
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := hashFile(filepath.Join(t.TempDir(), "no-such-file"))
		if err == nil {
			t.Error("expected error for missing file")
		}
	})
}

// TestAProcessOwnedByAnotherUserSaysSo is the failure seen on a live Pi: a
// system started with sudo, for the GPIO access it needs, could never be
// attested — Linux lets a process read another's /proc/<pid>/exe only if it
// could trace it. Every other system on the host certified; that one retried
// once a minute for as long as it ran, and the only clue was "Cannot resolve
// executable for PID", which names neither the cause nor a remedy.
func TestAProcessOwnedByAnotherUserSaysSo(t *testing.T) {
	tr := &Traits{}

	withResolveExecutable(t, func(pid int) (string, error) {
		return "", &fs.PathError{Op: "readlink", Path: "/proc/1234/exe", Err: syscall.EACCES}
	})

	body, _ := json.Marshal(attestationRequest{PID: 1234, Nonce: "0123456789abcdef0123456789abcdef"})
	w := httptest.NewRecorder()
	tr.attest(w, httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body)))

	// A refusal, not a fault of maitreD's: it will never be able to see this
	// process, so there is nothing to retry.
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}

	said := w.Body.String()
	// The process number and the cause, not the Linux path: on Windows and
	// macOS the path is read another way and the sentence differs.
	for _, want := range []string{"another user", "1234"} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal does not mention %q: %s", want, strings.TrimSpace(said))
		}
	}
	// The remedy has to point away from privilege, not towards it: a maitreD
	// running as root to inspect everything is a larger thing to trust than the
	// systems it attests.
	if strings.Contains(said, "sudo") && !strings.Contains(said, "rather than sudo") {
		t.Errorf("the refusal recommends sudo: %s", strings.TrimSpace(said))
	}
}

// TestAProcessThatExitedIsNotAFault: the other resolution failure, which is
// nothing to worry about and must not read like the one above.
func TestAProcessThatExitedIsNotAFault(t *testing.T) {
	tr := &Traits{}

	withResolveExecutable(t, func(pid int) (string, error) {
		return "", &fs.PathError{Op: "readlink", Path: "/proc/1234/exe", Err: syscall.ENOENT}
	})

	body, _ := json.Marshal(attestationRequest{PID: 1234, Nonce: "0123456789abcdef0123456789abcdef"})
	w := httptest.NewRecorder()
	tr.attest(w, httptest.NewRequest(http.MethodPost, "/attest", bytes.NewReader(body)))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "exited") {
		t.Errorf("a vanished process is not reported as such: %s", strings.TrimSpace(w.Body.String()))
	}
}
