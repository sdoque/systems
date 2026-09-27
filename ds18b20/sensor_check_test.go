package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A generated configuration ships the placeholder "sensor_Id", so this is the
// state every fresh deployment starts in. It must be told apart from a real id.
func TestPlaceholderNameIsRejectedWhenSensorsArePresent(t *testing.T) {
	v, ids := sensorVerdict("sensor_Id", []string{"28-000008717c93"})
	if v != wrongName {
		t.Errorf("verdict = %v, want wrongName", v)
	}
	if len(ids) != 1 || ids[0] != "28-000008717c93" {
		t.Errorf("the operator is not told what to write instead: %v", ids)
	}
}

func TestAMatchingNameIsAccepted(t *testing.T) {
	if v, _ := sensorVerdict("28-000008717c93", []string{"28-0000087aaaaa", "28-000008717c93"}); v != sensorOK {
		t.Errorf("verdict = %v, want sensorOK", v)
	}
}

// An empty bus is not a configuration error. The overlay may load late, or the
// probe may be unplugged and plugged back in, so the system must keep running
// and report no reading rather than exit.
func TestAnEmptyBusIsAWarningNotAFault(t *testing.T) {
	if v, _ := sensorVerdict("28-000008717c93", nil); v != busEmpty {
		t.Errorf("verdict = %v, want busEmpty", v)
	}
}

func TestSensorIDsReadsOnlyDS18B20s(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"28-000008717c93", "28-0000087aaaaa", "w1_bus_master1", "00-somethingelse"} {
		if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := oneWireDir
	oneWireDir = dir
	defer func() { oneWireDir = old }()

	got := sensorIDs()
	want := []string{"28-000008717c93", "28-0000087aaaaa"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v — the bus master is not a sensor", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

// A missing directory means no 1-wire support at all; it must not panic.
func TestAMissingBusDirectoryIsEmptyNotFatal(t *testing.T) {
	old := oneWireDir
	oneWireDir = filepath.Join(t.TempDir(), "no-such-bus")
	defer func() { oneWireDir = old }()
	if ids := sensorIDs(); len(ids) != 0 {
		t.Errorf("got %v, want none", ids)
	}
}
