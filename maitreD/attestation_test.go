package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// testCA mints a root the way the CA does, so a statement made in a test can be
// checked the way the CA checks one.
func testCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cert, key
}

// testTraitsWithCert builds Traits for an enrolled maitreD: a key and a
// certificate issued by the given CA, exactly what Husk carries after
// RequestCertificate.
func testTraitsWithCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (*Traits, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "maitreD"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	return &Traits{
		owner: &components.System{Husk: &components.Husk{Pkey: key, Certificate: certPEM}},
	}, key
}

// verifyWithKey checks a statement the way the CA does, so a change to the
// signed bytes on this side breaks a test here rather than attestation in the
// field.
func verifyWithKey(t *testing.T, st attestationStatement, pub *ecdsa.PublicKey) {
	t.Helper()
	sig, err := base64.StdEncoding.DecodeString(st.Signature)
	if err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	digest := sha256.Sum256(signedMessage(st.PID, st.Hash, st.Nonce, st.Timestamp))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Error("the statement does not verify against the maitreD's own key")
	}
}

// The signed bytes are a contract between two binaries that share no code, so
// the exact layout is pinned here. Changing it means changing the CA too.
func TestSignedMessageLayout(t *testing.T) {
	got := string(signedMessage(42, "abc", "nonce", "2026-09-08T00:00:00Z"))
	want := "mbaigo-attestation-v1\nnonce\n42\nabc\n2026-09-08T00:00:00Z"
	if got != want {
		t.Errorf("signed message is\n%q\nwant\n%q", got, want)
	}
}

// Every field must be covered, or a statement can be reused for a different
// question.
func TestSignedMessageCoversEveryField(t *testing.T) {
	base := string(signedMessage(42, "abc", "nonce", "ts"))
	for _, other := range []string{
		string(signedMessage(43, "abc", "nonce", "ts")),
		string(signedMessage(42, "xyz", "nonce", "ts")),
		string(signedMessage(42, "abc", "other", "ts")),
		string(signedMessage(42, "abc", "nonce", "later")),
	} {
		if other == base {
			t.Error("two different questions produce the same signed bytes")
		}
	}
}

func TestSignRefusesBeforeEnrolment(t *testing.T) {
	if _, err := sign(nil, "", 1, "hash", "nonce"); err == nil {
		t.Error("an unenrolled maitreD produced a statement; the CA could not tell it from anything else")
	}
}
