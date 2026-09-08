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
 *   Thomas Hedeler, Hamburg - initial implementation
 ***************************************************************************SDG*/

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// isMaitreDAuthorized reports whether ip is in the configured list of permitted maitreD hosts.
func (t *Traits) isMaitreDAuthorized(ip string) bool {
	for _, h := range t.MaitreDHosts {
		if h == ip {
			return true
		}
	}
	return false
}

// requestAttestation asks the maitreD on hostIP what is running as pid, checks
// that the answer really came from a maitreD this CA certified, and compares
// the measured hash against the whitelist. Returns nil only if all of that
// holds.
//
// The decision is made here, on the machine that owns whitelist.json. The
// maitreD is asked for a measurement, not for a verdict: it is the only thing
// that can see the process, and this is the only thing that should be deciding
// what is allowed to run.
//
// The channel is plain HTTP to an address the requesting system controls, and
// the maitreD's attest service is exempt from authorization because the
// bootstrap plane cannot require the tokens it exists to create. Neither can be
// helped, so neither is relied on: the signature is what makes the answer worth
// anything.
//
// hostIP comes from net.SplitHostPort on the requester's RemoteAddr, which strips
// the IPv6 brackets. We use net.JoinHostPort to put them back, otherwise a same-host
// request from the IPv6 loopback (::1) would build the malformed URL
// "http://::1:20101/..." that http.Post cannot parse.
func (t *Traits) requestAttestation(hostIP string, pid int) error {
	// Read the whitelist first. If the operator's policy cannot be read there
	// is no basis for a decision, and the honest outcome is to refuse rather
	// than to ask a question whose answer could not be judged.
	wl, err := loadWhitelist(t.WhitelistPath)
	if err != nil {
		return fmt.Errorf("cannot read the whitelist: %w", err)
	}

	nonce, err := newNonce()
	if err != nil {
		return fmt.Errorf("cannot generate a challenge: %w", err)
	}

	host := net.JoinHostPort(hostIP, strconv.Itoa(t.MaitreDPort))
	url := "http://" + host + "/maitreD/maitreD/attest"
	st, err := askMaitreD(http.DefaultClient, url, pid, nonce)
	if err != nil {
		return fmt.Errorf("cannot get an attestation from %s: %w", hostIP, err)
	}

	hash, err := verifyStatement(st, t.certificate, pid, nonce)
	if err != nil {
		return fmt.Errorf("attestation from %s not trusted: %w", hostIP, err)
	}

	if !approved(wl, hash) {
		return fmt.Errorf("executable hash %s is not in the whitelist", hash)
	}
	// Logged on success as well as on refusal, because a post-incident review
	// needs to know which binary was let in and under which version of the
	// policy — the version being the whitelist file's mtime.
	log.Printf("certify: attested pid=%d on %s: hash=%s (whitelist version %d)",
		pid, hostIP, hash, wl.Version)
	return nil
}

//-------------------------------------Define the unit asset

// Traits holds the configurable parameters for the certificate authority.
type Traits struct {
	privateKey    *ecdsa.PrivateKey  `json:"-"`
	certificate   *x509.Certificate  `json:"-"`
	SafeSWare     bool               `json:"safeSWare"`
	MaitreDHosts  []string           `json:"maitreDHosts"` // IPs permitted to enroll a maitreD
	MaitreDPort   int                `json:"maitreDPort"`  // port of the maitreD attest endpoint (0 = skip attestation)
	WhitelistPath string             `json:"-"`            // path to whitelist.json; defaults to "whitelist.json"
	owner         *components.System `json:"-"`
	name          string             `json:"-"`
}

// servedServices drops services this CA no longer answers.
//
// Configure reads the systemconfig.json that already exists, so removing a
// service from the template never reaches a host that has run before: a CA
// deployed at the cottage still lists "whitelist" and would register a service
// whose handler is gone. Filtering here means an existing deployment needs no
// hand-editing — which is the failure mode this project keeps rediscovering,
// most recently with Forms, Role, Platform and Location.
func servedServices(configured []components.Service) components.Services {
	svcs := usecases.MakeServiceMap(configured)
	if _, stale := svcs["whitelist"]; stale {
		delete(svcs, "whitelist")
		log.Println("ignoring the obsolete 'whitelist' service in systemconfig.json: the CA no longer serves its whitelist to anyone")
	}
	return svcs
}

//-------------------------------------Instantiate a unit asset template

// initTemplate initializes a UnitAsset with default values.
func initTemplate() *components.UnitAsset {
	certify := components.Service{
		Definition:  "certify",
		SubPath:     "certify",
		Details:     map[string][]string{"Forms": {"csr.pem"}, "Methods": components.HTTPMethods("POST")},
		RegPeriod:   30,
		Description: "signs a certificate signing request (POST) from authenticated systems in its local cloud",
	}

	return &components.UnitAsset{
		Name:     "certification",
		Mission:  components.MissionCore,
		Mobility: components.MobilityMovable,
		ServicesMap: map[string]*components.Service{
			certify.SubPath: &certify,
		},
		// Defaults are secure-by-default:
		//   - MaitreDHosts includes both loopback addresses so a CA and a
		//     maitreD running on the same host work out-of-the-box (the
		//     resolver may pick either IPv4 or IPv6 for "localhost").
		//     Operators add real LAN IPs as the deployment extends.
		//   - MaitreDPort is set to the standard mbaigo maitreD port (20101).
		//     Setting it to 0 silently disables attestation entirely; we
		//     refuse to ship that as the default. Operators who genuinely
		//     want to bypass attestation must set it to 0 deliberately.
		Traits: &Traits{
			MaitreDHosts: []string{"127.0.0.1", "::1"},
			MaitreDPort:  20101,
		},
	}
}

//-------------------------------------Instantiate unit asset(s) based on configuration

// newResource creates the unit asset with its pointers and channels based on the configuration.
func newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (*components.UnitAsset, func()) {
	t := &Traits{
		owner:         sys,
		name:          configuredAsset.Name,
		WhitelistPath: "whitelist.json",
	}

	if len(configuredAsset.Traits) > 0 {
		if err := json.Unmarshal(configuredAsset.Traits[0], t); err != nil {
			log.Println("Warning: could not unmarshal traits:", err)
		}
	}

	certFile := "ca_certificate.pem"
	keyFile := "ca_private_key.pem"

	var err error
	t.certificate, t.privateKey, err = ensureCertificate(sys, certFile, keyFile)
	if err != nil {
		log.Fatalf("Failed to ensure CA certificate and key: %v", err)
	}

	// Convert the certificate to PEM format and store in the system husk
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: t.certificate.Raw,
	})
	if certPEM == nil {
		log.Fatalf("failed to encode certificate to PEM format")
	}
	sys.Husk.Certificate = string(certPEM)
	// The CA serves its own HTTPS server using its self-signed root cert. The
	// framework's startHTTPSServer needs three Husk fields populated:
	//   Certificate — the server cert (already set above).
	//   Pkey         — the matching private key.
	//   CA_cert      — the trust root for verifying CLIENT certs in mTLS.
	//                  For the CA, this is its own cert: the CA trusts certs
	//                  it has signed, all of which chain back to itself.
	sys.Husk.Pkey = t.privateKey
	sys.Husk.CA_cert = string(certPEM)

	ua := &components.UnitAsset{
		Name:        configuredAsset.Name,
		Mission:     configuredAsset.Mission,
		Mobility:    configuredAsset.Mobility,
		TetheredTo:  configuredAsset.TetheredTo,
		Owner:       sys,
		Details:     configuredAsset.Details,
		ServicesMap: servedServices(configuredAsset.Services),
		Traits:      t,
	}
	ua.ServingFunc = func(w http.ResponseWriter, r *http.Request, servicePath string) {
		serving(t, w, r, servicePath)
	}

	return ua, func() {
		log.Println("shutting down certificate authority")
	}
}

//-------------------------------------Unit asset's function methods

// signCSR creates a certificate as an answer to the certificate signing request
func signCSR(csr *x509.CertificateRequest, caCert *x509.Certificate, caPrivateKey interface{}) ([]byte, error) {
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("invalid CSR signature: %v", err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}

	certTemplate := x509.Certificate{
		SerialNumber:          serialNumber,
		Subject:               csr.Subject,
		DNSNames:              csr.DNSNames,
		IPAddresses:           csr.IPAddresses,
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &certTemplate, caCert, csr.PublicKey, caPrivateKey)
	if err != nil {
		return nil, err
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes}), nil
}

func (t *Traits) certifying(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		return
	}

	csrPEM, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read CSR", http.StatusBadRequest)
		return
	}

	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		http.Error(w, "Failed to decode CSR", http.StatusBadRequest)
		return
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		http.Error(w, "Failed to parse CSR", http.StatusBadRequest)
		return
	}

	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "Failed to determine client IP", http.StatusInternalServerError)
		return
	}

	// maitreD systems may only enroll from pre-authorized host IPs.
	if csr.Subject.CommonName == "maitreD" {
		if !t.isMaitreDAuthorized(clientIP) {
			log.Printf("certify: denied maitreD enrollment from %q (maitreDHosts=%v)", clientIP, t.MaitreDHosts)
			http.Error(w, "Unauthorized maitreD host", http.StatusForbidden)
			return
		}
	} else if t.MaitreDPort != 0 {
		// All other systems require attestation from the maitreD on their host.
		pidStr := r.Header.Get("X-Process-PID")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 0 {
			http.Error(w, "Missing or invalid X-Process-PID header", http.StatusBadRequest)
			return
		}
		if err := t.requestAttestation(clientIP, pid); err != nil {
			log.Printf("certify: attestation failed for CN=%q from %q (pid=%d): %v",
				csr.Subject.CommonName, clientIP, pid, err)
			http.Error(w, "Attestation failed: "+err.Error(), http.StatusForbidden)
			return
		}
	} else {
		// MaitreDPort == 0: attestation is disabled. The CSR will be signed
		// without contacting the maitreD. This is convenient for early-stage
		// development and isolated testing, but it leaves the CA in a
		// "trust everything" mode that bypasses the security model
		// described in the paper. Log every such issuance so a post-incident
		// review can identify deauthorized binaries that nevertheless
		// obtained certs because attestation was disabled.
		log.Printf("WARNING: certify: attestation disabled (maitreDPort=0); signing CN=%q from %q without verification",
			csr.Subject.CommonName, clientIP)
	}

	signedCert, err := signCSR(csr, t.certificate, t.privateKey)
	if err != nil {
		http.Error(w, "Failed to sign CSR", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Write(signedCert)
}

func generateSelfSignedCert(sys *components.System) ([]byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}

	dnsNames := []string{"localhost"}
	var ipAddrs []net.IP
	for _, ipStr := range sys.Husk.Host.IPAddresses {
		ip := net.ParseIP(ipStr)
		if ip != nil {
			ipAddrs = append(ipAddrs, ip)
		}
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Synecdoque"},
			CommonName:   "synecdoque.com",
		},
		DNSNames:    dnsNames,
		IPAddresses: ipAddrs,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		// ExtKeyUsage on the CA root must include every purpose the certs it
		// signs will be used for. End-entity system certs in this cloud carry
		// both ServerAuth and ClientAuth (they serve mTLS *and* call mTLS).
		// Per RFC 5280, an issuer's ExtKeyUsage must be a superset of the
		// cert it signs. Without ClientAuth here, every mTLS handshake fails
		// with "x509: certificate specifies an incompatible key usage".
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})

	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return nil, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, nil
}

// ensureCertificate ensures that the CA certificate and key exist and are loaded.
func ensureCertificate(sys *components.System, certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	if _, err := os.Stat(certFile); err == nil {
		if _, err := os.Stat(keyFile); err == nil {
			return loadCACertificate(certFile, keyFile)
		}
	}

	certPEM, keyPEM, err := generateSelfSignedCert(sys)
	if err != nil {
		return nil, nil, err
	}

	if err = os.WriteFile(certFile, certPEM, 0644); err != nil {
		return nil, nil, err
	}
	if err = os.WriteFile(keyFile, keyPEM, 0644); err != nil {
		return nil, nil, err
	}
	log.Println("CA certificate and private key have been created")
	return loadCACertificate(certFile, keyFile)
}

// loadCACertificate attempts to load the CA's certificate and private key from files.
func loadCACertificate(certFile, keyFile string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEMBlock, err := os.ReadFile(certFile)
	if err != nil {
		return nil, nil, err
	}

	keyPEMBlock, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, nil, err
	}

	certBlock, _ := pem.Decode(certPEMBlock)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("failed to parse certificate PEM")
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	keyBlock, _ := pem.Decode(keyPEMBlock)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("failed to parse key PEM")
	}
	caPrivateKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	log.Println("CA certificate and private key have been loaded")
	return caCert, caPrivateKey, nil
}
