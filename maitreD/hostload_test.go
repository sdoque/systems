package main

import (
	"path/filepath"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
)

// useFixtures points the readers at files this test controls, so the parsing is
// exercised on a Mac as well as on the Pi it will run on.
func useFixtures(t *testing.T) {
	t.Helper()
	base := filepath.Join("testdata", "proc")
	loadAvg, meminfo, pressureDir, thermal := procLoadAvg, procMeminfo, procPressure, sysThermal
	procLoadAvg = filepath.Join(base, "loadavg")
	procMeminfo = filepath.Join(base, "meminfo")
	procPressure = filepath.Join(base, "pressure")
	sysThermal = filepath.Join("testdata", "thermal_temp")
	t.Cleanup(func() {
		procLoadAvg, procMeminfo, procPressure, sysThermal = loadAvg, meminfo, pressureDir, thermal
	})
}

func TestReadingTheHost(t *testing.T) {
	useFixtures(t)
	f, measured := sample("canbus")
	if !measured {
		t.Fatal("the fixtures were not read")
	}

	if f.Load1 != 3.80 || f.Load15 != 1.05 {
		t.Errorf("load averages %v %v %v", f.Load1, f.Load5, f.Load15)
	}
	// MemAvailable, not MemFree: a healthy Linux machine that has read a few
	// files has almost no free memory and plenty available, and reporting the
	// former would make every host look exhausted.
	if f.MemAvailableMB != 1000 {
		t.Errorf("available memory %d MB; want MemAvailable rather than MemFree", f.MemAvailableMB)
	}
	if f.CPUTempC < 56 || f.CPUTempC > 57 {
		t.Errorf("temperature %.1f C", f.CPUTempC)
	}
	if f.StallCPU == nil || *f.StallCPU != 42.5 {
		t.Errorf("cpu stall %v", f.StallCPU)
	}
	// No io fixture: the kernel not reporting one must stay distinguishable
	// from it reporting zero.
	if f.StallIO != nil {
		t.Errorf("io stall = %v; the fixture has no io file, so it was not measured", *f.StallIO)
	}
}

// A host that cannot measure itself must not report itself idle.
//
// Found by running the sampler on a Mac, which has no /proc: every figure came
// back zero and headroom read 1.0 — "completely free" — which is the most
// dangerous answer available, because a balancer would move work onto the one
// machine that cannot say how loaded it is.
func TestAHostThatCannotMeasureItselfSaysSo(t *testing.T) {
	loadAvg := procLoadAvg
	procLoadAvg = filepath.Join("testdata", "does-not-exist")
	t.Cleanup(func() { procLoadAvg = loadAvg })

	if _, measured := sample("nowhere"); measured {
		t.Error("a host with no readable load average claimed to have measured itself")
	}
}

// Headroom takes the worst constraint, not the average of them. A machine with
// idle CPUs and no memory is not half-available; it is unavailable.
func TestHeadroomIsTheWorstConstraint(t *testing.T) {
	stall := func(v float64) *float64 { return &v }

	full := forms1(t, 0, 8000, 8000, stall(0))
	if got := headroom(&full); got < 0.99 {
		t.Errorf("an idle host has headroom %.2f", got)
	}

	// Plenty of CPU, almost no memory.
	starved := forms1(t, 0, 100, 8000, stall(0))
	if got := headroom(&starved); got > 0.05 {
		t.Errorf("a host with 100 MB of 8000 free has headroom %.2f; memory is the binding constraint", got)
	}

	// Busy CPU, plenty of memory.
	busy := forms1(t, 0, 8000, 8000, stall(95))
	if got := headroom(&busy); got > 0.1 {
		t.Errorf("a host stalling 95%% of the time has headroom %.2f", got)
	}
}

// Thermal throttling is not a degree of busyness. A Raspberry Pi that has
// derated is delivering less than its load average implies, and a balancer that
// reads it as quiet moves work onto a machine that cannot do it.
func TestAThrottledHostIsNotAQuietHost(t *testing.T) {
	quiet := forms1(t, 0, 8000, 8000, nil)
	quiet.Load1, quiet.Cores, quiet.LoadNormalized = 0.1, 4, 0.025
	before := headroom(&quiet)

	quiet.ThrottledNow = true
	after := headroom(&quiet)

	if !(after < before) {
		t.Errorf("throttling did not reduce headroom: %.2f then %.2f", before, after)
	}
	if after > 0.2 {
		t.Errorf("a throttled host reports headroom %.2f; it is not available", after)
	}
}

// forms1 builds a reading for the headroom tests.
func forms1(t *testing.T, load float64, availMB, totalMB int, cpuStall *float64) forms.HostLoad_v1 {
	t.Helper()
	var f forms.HostLoad_v1
	f.NewForm()
	f.Cores = 4
	f.Load1, f.LoadNormalized = load, load/4
	f.MemAvailableMB, f.MemTotalMB = availMB, totalMB
	f.StallCPU = cpuStall
	return f
}

// loadstatus must be gated by the authorizer, and attest must not.
//
// The bootstrap exemption serves a core-mission service without a token,
// because the plane that makes tokens possible cannot require one. Attestation
// is squarely there. Reporting how busy a machine is is not: it is a
// measurement, and it is also reconnaissance — which host is loaded, when, and
// how the cloud's work is distributed. EffectiveMission takes a service's own
// mission over its asset's, so one field separates them.
func TestLoadstatusIsBehindTheAuthorizerAndAttestIsNot(t *testing.T) {
	ua := initTemplate()
	services := ua.GetServices()

	attest, ok := services["attest"]
	if !ok {
		t.Fatal("no attest service")
	}
	if got := components.EffectiveMission(ua, attest); got != components.MissionCore {
		t.Errorf("attest has effective mission %q; certification depends on it answering "+
			"before any token exists", got)
	}

	load, ok := services["loadstatus"]
	if !ok {
		t.Fatal("no loadstatus service")
	}
	if got := components.EffectiveMission(ua, load); got == components.MissionCore {
		t.Error("loadstatus is core-mission, so the bootstrap exemption serves it to " +
			"anything the CA has enrolled, without a token")
	}
	if !load.SubscribeAble {
		t.Error("loadstatus is not subscribable, so a balancer must poll every host " +
			"for a number that changes slowly")
	}
}
