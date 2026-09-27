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

// Connecting to an OPC UA server with encryption rather than in the clear.
//
// A secure channel is mutual: the client must present an application instance
// certificate, and the server must accept it. That certificate is the reason
// encryption so often stays switched off — producing one is a task somebody has
// to do, and it arrives at commissioning time when there is other work.
//
// So this generates one on first run if none exists, with the application URI
// in the subject alternative name as OPC UA requires. Whether the server then
// trusts it is the server's decision and not ours: a server configured to
// accept clients automatically will take it, and one with a curated trust list
// will refuse until an engineer adds it. The refusal is the honest outcome and
// the log says which happened.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"
)

// ensureClientCertificate returns the paths to this system's OPC UA application
// instance certificate, creating a self-signed one if it is not there yet.
//
// Self-signed because there is nothing to chain to that an OPC UA server would
// recognise: the cloud's own CA is not in any PLC's trust list, and putting it
// there is the manual step this framework is trying to remove elsewhere. When
// that becomes possible — a server implementing push certificate management, or
// a device onboarded with a manufacturer identity — this is the function to
// change.
func ensureClientCertificate(dir, appURI string) (certPath, keyPath string, err error) {
	certPath = filepath.Join(dir, "opcua-client-cert.pem")
	keyPath = filepath.Join(dir, "opcua-client-key.pem")
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return certPath, keyPath, nil
		}
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", fmt.Errorf("generating the OPC UA client key: %w", err)
	}
	uri, err := url.Parse(appURI)
	if err != nil {
		return "", "", fmt.Errorf("the application URI %q is not a URI: %w", appURI, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   "mbaigo uaclient",
			Organization: []string{"Synecdoque"},
		},
		NotBefore: time.Now().Add(-time.Hour),
		NotAfter:  time.Now().Add(5 * 365 * 24 * time.Hour),
		// The usages an OPC UA application instance certificate must carry; a
		// server checking them will reject a certificate that omits them, and
		// the error it returns rarely says which one was missing.
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDataEncipherment | x509.KeyUsageContentCommitment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		// Required by the specification: the application URI must appear here,
		// and must match the URI the client announces, or the server refuses.
		URIs: []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("creating the OPC UA client certificate: %w", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		return "", "", err
	}
	log.Printf("uaclient: generated an OPC UA client certificate at %s\n", certPath)
	log.Println("uaclient: if the server keeps a trust list, this certificate has to be added to it")
	return certPath, keyPath, nil
}

// clientOptions turns the configured security settings into connection options.
//
// An empty or "None" policy connects in the clear, which is what an
// unconfigured server offers and what this system did before. Anything else
// needs the certificate above.
func clientOptions(t Traits, endpoints []*ua.EndpointDescription, dir string) ([]opcua.Option, string, error) {
	policy := t.SecurityPolicy
	if policy == "" {
		policy = "None"
	}
	mode := t.SecurityMode
	if mode == "" {
		mode = "None"
	}
	if policy == "None" && mode == "None" {
		return nil, "None/None (in the clear)", nil
	}

	appURI := t.ApplicationURI
	if appURI == "" {
		appURI = defaultApplicationURI
	}
	certPath, keyPath, err := ensureClientCertificate(dir, appURI)
	if err != nil {
		return nil, "", err
	}

	secMode := ua.MessageSecurityModeFromString(mode)
	policyURI := "http://opcfoundation.org/UA/SecurityPolicy#" + policy
	ep, err := opcua.SelectEndpoint(endpoints, policyURI, secMode)
	if err != nil || ep == nil {
		return nil, "", fmt.Errorf("the server offers no %s/%s endpoint", policy, mode)
	}
	return []opcua.Option{
		opcua.SecurityFromEndpoint(ep, ua.UserTokenTypeAnonymous),
		opcua.CertificateFile(certPath),
		opcua.PrivateKeyFile(keyPath),
		opcua.ApplicationURI(appURI),
	}, policy + "/" + mode, nil
}

// defaultApplicationURI identifies this client to an OPC UA server. It must
// match the SAN in the certificate above.
const defaultApplicationURI = "urn:sdoque:mbaigo:uaclient"
