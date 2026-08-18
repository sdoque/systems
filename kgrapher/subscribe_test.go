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

// TestASilentStreamIsTreatedAsDead is the failure "it never gives up" did not
// cover.
//
// A NAT idle timeout, a partition, or a registrar killed without a FIN leaves
// the subscriber blocked in a read that never returns. followOnce never returns,
// so follow never reconnects, and the graph goes on describing a cloud this
// system stopped watching — with no log line, because nothing failed. It is the
// one shape of failure that produces no evidence.
//
// The registry writes a keep-alive comment on an idle stream, so silence past
// the limit is a dead connection rather than a quiet cloud. This drives it with
// a body that never ends and never says anything.
func TestASilentStreamIsTreatedAsDead(t *testing.T) {
	// A body that blocks until the test is done: the stream is open and mute.
	done := make(chan struct{})
	defer close(done)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(blockingReader{done}),
	}

	// A short limit so the watchdog itself is what fires. Driving this with the
	// context instead would pass whether or not the watchdog existed, which is
	// no test of it at all.
	tr := &Traits{rebuilding: func() {}, silenceFor: 200 * time.Millisecond}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	read := make(chan error, 1)
	go func() { read <- tr.read(ctx, resp) }()

	select {
	case err := <-read:
		if err == nil {
			t.Fatal("a silent stream ended without an error, so follow would not reconnect")
		}
		if !strings.Contains(err.Error(), "keep-alive") {
			t.Errorf("the stream ended with %q; the watchdog is what should have ended "+
				"it, and its reason is what tells an operator the connection died", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read never returned on a stream that said nothing: the subscriber is " +
			"stuck and the graph is stale with nothing in the log to say so")
	}
}

// blockingReader is a stream that stays open and never speaks.
type blockingReader struct{ done <-chan struct{} }

func (b blockingReader) Read(p []byte) (int, error) {
	<-b.done
	return 0, io.EOF
}

// The watchdog has to be reset by the registry's keep-alive comments, or a
// healthy but idle stream is torn down every silenceLimit — which is worse than
// no watchdog, because a reconnection re-reads the whole registry and every
// system's ontology with it.
func TestAKeepAliveKeepsTheStreamOpen(t *testing.T) {
	if silenceLimit <= keepAliveExpectation {
		t.Errorf("silenceLimit is %s and the registry writes a keep-alive every %s; "+
			"a healthy stream would be torn down between beats", silenceLimit, keepAliveExpectation)
	}
	// Room for a missed beat rather than exactly one interval, so a slow moment
	// does not cost a reconnection and a full rebuild.
	if silenceLimit < 2*keepAliveExpectation {
		t.Errorf("silenceLimit is %s, less than two keep-alive intervals (%s): one "+
			"missed beat reconnects the subscriber", silenceLimit, keepAliveExpectation)
	}
}

// keepAliveExpectation is the registrar's keep-alive interval as this subscriber
// assumes it. The registry defines it; stated here so a change to either one
// that breaks the relationship fails a test rather than a deployment.
const keepAliveExpectation = 20 * time.Second
