package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// eventStream is a registry that emits a snapshot and then whatever changes the
// test asks for, holding the connection open the way the real one does.
type eventStream struct {
	body string
}

func (e eventStream) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		Status: "200 OK", StatusCode: 200,
		Header:  http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:    io.NopCloser(strings.NewReader(e.body)),
		Request: req,
	}, nil
}

func changeEvent(system, definition, change string) string {
	return fmt.Sprintf("event: change\ndata: {\"change\":%q,\"record\":{\"systemName\":%q,"+
		"\"definition\":%q,\"version\":\"ServiceRecord_v1\"},\"version\":\"RegistryEvent_v1\"}\n\n",
		change, system, definition)
}

// TestABurstOfRegistrationsRebuildsOnce is the property the design rests on.
//
// A system starting up registers its services one at a time, so an arrival is
// several events a few milliseconds apart. Rebuilding on each would cost one
// request to the registrar and one to every system in the cloud, several times
// over, for a single arrival. The subscriber waits for the registry to go quiet
// instead.
func TestABurstOfRegistrationsRebuildsOnce(t *testing.T) {
	var rebuilds atomic.Int32

	stream := "event: snapshot\ndata: {\"systemurl\":[],\"version\":\"SystemRecordList_v1\"}\n\n"
	for _, s := range []string{"setpoint", "deviation", "jitter"} {
		stream += changeEvent("thermostat", s, "registered")
	}

	tr := &Traits{rebuilding: func() { rebuilds.Add(1) }}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resp, err := eventStream{body: stream}.RoundTrip(httptest.NewRequest(http.MethodGet, "/syslist", nil))
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	read := make(chan error, 1)
	go func() { read <- tr.read(ctx, resp) }()

	// The stream ends after the burst, so read returns; the settle timer is
	// still pending at that point, which is the case worth checking — a rebuild
	// owed must not be lost because the connection closed.
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not return after the stream ended")
	}

	if got := rebuilds.Load(); got != 1 {
		t.Errorf("four events produced %d rebuilds; a burst owes exactly one, and a"+
			" connection dropping before the registry goes quiet does not cancel it", got)
	}
}

// The registry going quiet is what triggers the rebuild, and it happens once for
// however many events arrived before it.
func TestTheGraphIsRebuiltAfterTheRegistrySettles(t *testing.T) {
	var rebuilds atomic.Int32
	tr := &Traits{rebuilding: func() { rebuilds.Add(1) }}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A stream that stays open: the reader blocks on it, so the settle timer
	// fires while the connection is still alive, as it does in a running cloud.
	pr, pw := io.Pipe()
	defer pw.Close()
	resp := &http.Response{StatusCode: 200, Body: pr}

	go func() { _ = tr.read(ctx, resp) }()

	for _, s := range []string{"setpoint", "deviation", "jitter"} {
		if _, err := io.WriteString(pw, changeEvent("thermostat", s, "registered")); err != nil {
			t.Error(err)
			return
		}
	}

	deadline := time.Now().Add(settleFor + 3*time.Second)
	for time.Now().Before(deadline) {
		if rebuilds.Load() == 1 {
			return // settled into exactly one rebuild
		}
		if n := rebuilds.Load(); n > 1 {
			t.Fatalf("three registrations produced %d rebuilds, want 1", n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the registry went quiet but the graph was never rebuilt (%d rebuilds)", rebuilds.Load())
}
