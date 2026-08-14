package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
)

// signalTransport answers every service call with a temperature, so the real
// control loop can run without a sensor or a servo.
type signalTransport struct{}

func (signalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		Status: "200 OK", StatusCode: 200,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"value":19.5,"unit":"<http://qudt.org/vocab/unit/DEG_C>","timestamp":"2026-08-14T18:00:00Z","version":"SignalA_v1.0"}`)),
		Request: req,
	}, nil
}

// TestSetpointAndDeviationAreRaceFree is follow-up finding N6, and it runs the
// real processFeedbackLoop rather than a stand-in: the defect is that the loop
// and the HTTP handlers touch the same four scalars, so a test whose "loop"
// takes no lock would only prove something about the test.
//
// setSetPoint writes SetPt on a net/http goroutine while the loop reads it; the
// loop writes deviation and jitter while the diff and variations handlers read
// them. It matters more than the runtime warning suggests: these run on
// Raspberry Pis, and a 32-bit build stores a float64 in two words, so a setpoint
// moving 20 to 22 while the loop reads it can yield a number that was never
// written — which calculateOutput turns into a fully open or fully closed valve.
func TestSetpointAndDeviationAreRaceFree(t *testing.T) {
	http.DefaultClient.Transport = signalTransport{}

	sys := components.NewSystem("thermostat", context.Background())
	sys.Husk = &components.Husk{ProtoPort: map[string]int{"http": 20101}}

	node := components.NodeInfo{
		URL:     "http://provider/service",
		Details: map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/PERCENT>"}},
		Tokens:  map[string]string{"read": "", "write": ""},
	}
	cervice := func(def string) *components.Cervice {
		return &components.Cervice{
			Definition: def,
			Details:    map[string][]string{"Unit": {"<http://qudt.org/vocab/unit/PERCENT>"}},
			Nodes:      map[string][]components.NodeInfo{"n": {node}},
			Protos:     []string{"http"},
		}
	}

	tr := &Traits{
		SetPt: 20, Period: 1, Kp: 5,
		owner:        &sys,
		cervices:     components.Cervices{"temperature": cervice("temperature"), "rotation": cervice("rotation")},
		setpointUnit: "<http://qudt.org/vocab/unit/DEG_C>",
		errorUnit:    "<http://qudt.org/vocab/unit/DEG_C>",
		jitterUnit:   "<http://qudt.org/vocab/unit/MilliSEC>",
	}

	var callers sync.WaitGroup
	stop := make(chan struct{})
	looped := make(chan struct{})

	// The real loop, run as fast as it will go.
	go func() {
		defer close(looped)
		for {
			select {
			case <-stop:
				return
			default:
				tr.processFeedbackLoop()
			}
		}
	}()

	// The HTTP side: set the target, read the deviation and the jitter.
	for i := 0; i < 4; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for j := 0; j < 50; j++ {
				body := `{"value":22.5,"unit":"<http://qudt.org/vocab/unit/DEG_C>","version":"SignalA_v1.0"}`
				put := httptest.NewRequest(http.MethodPut, "/thermostat/c/setpoint", strings.NewReader(body))
				put.Header.Set("Content-Type", "application/json")
				tr.setpt(httptest.NewRecorder(), put)

				tr.diff(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/thermostat/c/deviation", nil))
				tr.variations(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/thermostat/c/jitter", nil))
			}
		}()
	}

	callers.Wait()
	close(stop)
	select {
	case <-looped:
	case <-time.After(10 * time.Second):
		t.Fatal("the control loop did not stop; the guard may have deadlocked it")
	}

	if got := tr.getSetPoint(); got.Value != 22.5 {
		t.Errorf("setpoint is %v after the writes, want 22.5", got.Value)
	}
}
