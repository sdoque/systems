package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func TestInitTemplate(t *testing.T) {
	ua := initTemplate()

	if ua.Mission != components.MissionCore {
		t.Errorf("mission = %q; want %q", ua.Mission, components.MissionCore)
	}
	// The framework invariant: a system that registers nothing cannot be seen,
	// audited, or reasoned about — the authorizer least of all.
	if len(ua.ServicesMap) == 0 {
		t.Fatal("the authorizer provides no service")
	}
	if _, ok := ua.ServicesMap["authorize"]; !ok {
		t.Error("the authorize service is missing from the template")
	}
}

// A missing policy file means "nothing is authorised yet", not "start refusing
// to run": an uncommissioned cloud must be able to boot.
func TestReloadPoliciesToleratesAMissingFile(t *testing.T) {
	inDir(t, t.TempDir())

	tr := &Traits{}
	if err := tr.reloadPolicies(); err != nil {
		t.Fatalf("reloadPolicies with no file: %v", err)
	}
	if len(tr.currentPolicies().Rules) != 0 {
		t.Error("a missing file produced rules")
	}
}

func TestReloadPoliciesReadsAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	path := filepath.Join(dir, PoliciesFile)

	write(t, path, `{"policies":[{"subject":"a","missions":["measurement"],"actions":["read"]}]}`)

	tr := &Traits{}
	if err := tr.reloadPolicies(); err != nil {
		t.Fatalf("reloadPolicies: %v", err)
	}
	if got := len(tr.currentPolicies().Rules); got != 1 {
		t.Fatalf("loaded %d rules; want 1", got)
	}

	// An edit must take effect for subsequent decisions, or POLICY.md's
	// revocation semantics are not true.
	write(t, path, `{"policies":[
		{"subject":"a","missions":["measurement"],"actions":["read"]},
		{"subject":"b","missions":["actuation"],"actions":["write"]}]}`)
	bumpModTime(t, path)

	if got := len(tr.currentPolicies().Rules); got != 2 {
		t.Errorf("after an edit there are %d rules; want 2", got)
	}
}

// A typo must not take a plant down by silently reverting to "deny everything".
func TestReloadPoliciesKeepsPreviousRulesOnABadEdit(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	path := filepath.Join(dir, PoliciesFile)

	write(t, path, `{"policies":[{"subject":"a","missions":["measurement"],"actions":["read"]}]}`)
	tr := &Traits{}
	if err := tr.reloadPolicies(); err != nil {
		t.Fatalf("reloadPolicies: %v", err)
	}

	write(t, path, `{"policies":[{"subject":"a","missions":["measurment"],"actions":["read"]}]}`)
	bumpModTime(t, path)

	if err := tr.reloadPolicies(); err == nil {
		t.Error("an unknown mission was accepted")
	}
	if got := len(tr.currentPolicies().Rules); got != 1 {
		t.Errorf("a bad edit left %d rules; want the previous 1", got)
	}
}

// Adjudicate must account for every candidate, once, as either a grant or a
// refusal — a candidate that vanishes silently is one nobody can debug.
func TestAdjudicateAccountsForEveryCandidate(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	// The file is the source of truth: rules set only in memory are replaced on
	// the next reload, so the test states them where the authorizer reads them.
	write(t, filepath.Join(dir, PoliciesFile),
		`{"policies":[{"subject":"thermostat","missions":["measurement"],"actions":["read"]}]}`)

	tr := &Traits{attributesOf: func(string) map[string][]string { return nil }}
	tr.owner = enrolledSystem(t)

	kitchen := record("ds18b20", "sensor_Id", "temperature", "measurement", nil)
	valve := record("parallax", "Servo_1", "rotation", "actuation", nil)

	answer := tr.Adjudicate(forms.AuthorizationQuest_v1{
		Subject:    "thermostat",
		Action:     ActionRead,
		Candidates: []forms.ServiceRecord_v1{kitchen, valve},
	})

	if len(answer.Grants) != 1 {
		t.Fatalf("got %d grants; want 1", len(answer.Grants))
	}
	if len(answer.Refusals) != 1 {
		t.Fatalf("got %d refusals; want 1", len(answer.Refusals))
	}
	if answer.Grants[0].Record.SystemName != "ds18b20" {
		t.Errorf("granted %q; want ds18b20", answer.Grants[0].Record.SystemName)
	}
	if answer.Refusals[0].ProviderName != "parallax" {
		t.Errorf("refused %q; want parallax", answer.Refusals[0].ProviderName)
	}
	if answer.Refusals[0].Reason == "" {
		t.Error("a refusal must say why")
	}
	if answer.Grants[0].TTL != "5m0s" {
		t.Errorf("TTL = %q; want the default 5m", answer.Grants[0].TTL)
	}
	// The grant must be provable: a permission no provider can check is not one.
	claims, err := usecases.VerifyToken(answer.Grants[0].Token, &tr.owner.Husk.Pkey.PublicKey,
		usecases.TokenRequest{
			Subject:  "thermostat",
			Provider: "ds18b20",
			Asset:    "sensor_Id",
			Service:  "temperature",
			Action:   ActionRead,
		}, time.Now())
	if err != nil {
		t.Fatalf("the issued token does not verify: %v", err)
	}
	if claims.Issuer != "authorizer" {
		t.Errorf("issuer = %q; want the authorizer's own name", claims.Issuer)
	}
	if answer.FormVersion() != "AuthorizationGrantList_v1" {
		t.Errorf("version = %q", answer.FormVersion())
	}
}

// With no policies loaded, every candidate is refused and none is granted.
func TestAdjudicateDeniesWithoutPolicies(t *testing.T) {
	inDir(t, t.TempDir())

	tr := &Traits{attributesOf: func(string) map[string][]string { return nil }}
	tr.owner = enrolledSystem(t)
	answer := tr.Adjudicate(forms.AuthorizationQuest_v1{
		Subject:    "thermostat",
		Action:     ActionRead,
		Candidates: []forms.ServiceRecord_v1{record("ds18b20", "sensor_Id", "temperature", "measurement", nil)},
	})

	if len(answer.Grants) != 0 {
		t.Errorf("an uncommissioned authorizer granted %d candidates", len(answer.Grants))
	}
	if len(answer.Refusals) != 1 {
		t.Errorf("got %d refusals; want 1", len(answer.Refusals))
	}
}

func TestWritePoliciesTemplateIsLoadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), PoliciesFile)
	if err := writePoliciesTemplate(path); err != nil {
		t.Fatalf("writePoliciesTemplate: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if _, err := LoadPolicies(data); err != nil {
		t.Errorf("the starting point does not load: %v", err)
	}
	if !strings.Contains(string(data), "FunctionalLocation") {
		t.Error("the starting point does not demonstrate attribute pairing")
	}
}

// ---- helpers ----

// inDir runs the test from dir, since the policy file is read from the
// authorizer's working directory.
func inDir(t *testing.T, dir string) {
	t.Helper()
	was, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(was) })
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// bumpModTime makes an edit detectable even when both writes land within the
// filesystem's timestamp resolution.
func bumpModTime(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	future := info.ModTime().Add(time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// enrolledSystem is an authorizer that has finished enrolling with the CA and so
// holds the in-memory key it signs tokens with.
func enrolledSystem(t *testing.T) *components.System {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	sys := components.NewSystem("authorizer", context.Background())
	sys.Husk = &components.Husk{Pkey: key}
	return &sys
}

// An authorizer still enrolling cannot sign, and a permission no provider can
// check is not a permission. It must refuse rather than grant something hollow.
func TestAdjudicateRefusesBeforeItCanSign(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	write(t, filepath.Join(dir, PoliciesFile),
		`{"policies":[{"subject":"thermostat","missions":["measurement"],"actions":["read"]}]}`)

	tr := &Traits{attributesOf: func(string) map[string][]string { return nil }} // no owner, so no key

	answer := tr.Adjudicate(forms.AuthorizationQuest_v1{
		Subject:    "thermostat",
		Action:     ActionRead,
		Candidates: []forms.ServiceRecord_v1{record("ds18b20", "sensor_Id", "temperature", "measurement", nil)},
	})

	if len(answer.Grants) != 0 {
		t.Errorf("granted %d candidates without a signing key", len(answer.Grants))
	}
	if len(answer.Refusals) != 1 || !strings.Contains(answer.Refusals[0].Reason, "signing key") {
		t.Errorf("refusal does not explain the missing key: %+v", answer.Refusals)
	}
}
