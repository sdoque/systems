package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

// ------------------------------------------------ //
// Help functions and other goodies for testing
// ------------------------------------------------ //

// Create a error reader to break json.Unmarshal()
type errReader int

var errBodyRead error = fmt.Errorf("bad body read")

func (errReader) Read(p []byte) (n int, err error) {
	return 0, errBodyRead
}
func (errReader) Close() error {
	return nil
}

func createConfAssetMultipleTraits() usecases.ConfigurableAsset {
	uac := usecases.ConfigurableAsset{
		Name:     "testRegistrar",
		Details:  map[string][]string{"testDetail": {"detail1", "detail2"}},
		Services: []components.Service{},
		Traits:   []json.RawMessage{json.RawMessage(`{"recCount": 0}`), json.RawMessage(`{"leading": false}`)},
	}
	return uac
}

func createTestSystem() components.System {
	ctx := context.Background()
	sys := components.NewSystem("testsys", ctx)
	sys.Husk = &components.Husk{
		Description: " is for testing purposes",
		Certificate: "ABCD",
		Details:     map[string][]string{"Developer": {"Arrowhead"}},
		ProtoPort:   map[string]int{"https": 0, "http": 8870, "coap": 0},
		InfoLink:    "https://for.testing.purposes",
		Host:        components.NewDevice(),
	}
	leadingRegistrar := &components.CoreSystem{
		Name: components.ServiceRegistrarName,
		Url:  "https://leadingregistrar:1234",
	}
	orchestrator := &components.CoreSystem{
		Name: "orchestrator",
		Url:  "https://orchestator:1234",
	}
	sys.Husk.CoreS = []*components.CoreSystem{
		leadingRegistrar,
		orchestrator,
	}
	return sys
}

// --------------------------------------------------------------------------- //
// Help functions and structs to test the add part of serviceRegistryHandler()
// --------------------------------------------------------------------------- //

func createNewSys() components.System {
	// prepare for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background()) // create a context that can be canceled
	defer cancel()                                          // make sure all paths cancel the context to avoid context leak

	// instantiate the System
	sys := components.NewSystem("serviceregistrar", ctx)

	// Instantiate the Capsule
	sys.Husk = &components.Husk{
		Description: "is an Arrowhead mandatory core system that keeps track of the currently available services.",
		Details:     map[string][]string{"Developer": {"Synecdoque"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20102, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/esr",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"Synecdoque"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
		RegistrarChan: make(chan *components.CoreSystem, 1),
		Messengers:    make(map[string]int),
	}

	// instantiate a template unit asset
	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = assetTemplate
	return sys
}

func sendAddRequest(id int64, def string, subPath string, created string, ch chan ServiceRegistryRequest) error {
	rec := &forms.ServiceRecord_v1{
		Id:                int(id),
		ServiceDefinition: def,
		SystemName:        "System",
		ServiceNode:       "node",
		IPAddresses:       []string{"123.456.789.012"},
		ProtoPort:         map[string]int{"http": 1234},
		Details:           map[string][]string{"details": {}},
		Certificate:       "ABCD",
		SubPath:           subPath,
		RegLife:           25,
		Version:           "SignalA_v1a",
		Created:           created,
		Updated:           time.Now().String(),
		EndOfValidity:     time.Now().Add(25 * time.Second).String(),
		SubscribeAble:     false,
		ACost:             float64(id),
		CUnit:             "",
	}

	req := ServiceRegistryRequest{
		Action: "add",
		Record: rec,
		Error:  make(chan error),
	}

	ch <- req

	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

func sendBrokenAddRequest(num int64, ch chan ServiceRegistryRequest) error {
	rec := &forms.SignalA_v1a{}
	req := ServiceRegistryRequest{
		Action: "add",
		Record: rec,
		Id:     num,
		Error:  make(chan error),
	}
	ch <- req

	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

type serviceRegistryHandlerParams struct {
	expectError bool
	request     func(*Traits) error
	testCase    string
}

func TestServiceRegistryHandlerAdd(t *testing.T) {
	params := []serviceRegistryHandlerParams{
		{
			false,
			func(ua *Traits) error {
				return sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
			},
			"Best case, successful request",
		},
		{
			true,
			func(ua *Traits) error { return sendBrokenAddRequest(0, ua.requests) },
			"Bad case, unable to convert to correct form",
		},
		{
			false,
			// Registered afresh since 30 August 2026: an id this registrar
			// holds for another record was issued elsewhere, before a failover.
			func(ua *Traits) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef2", "subP", time.Now().Format(time.RFC3339), ua.requests)
				return err
			},
			"Good case (registered afresh), exists with different service definition",
		},
		{
			false,
			// Registered afresh since 30 August 2026: an id this registrar
			// holds for another record was issued elsewhere, before a failover.
			func(ua *Traits) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subPa", time.Now().Format(time.RFC3339), ua.requests)
				return err
			},
			"Good case (registered afresh), exists with different subpath",
		},
		{
			false,
			// Registered afresh since 30 August 2026: an id this registrar
			// holds for another record was issued elsewhere, before a failover.
			func(ua *Traits) error {
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ua.requests)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", "", ua.requests)
				return err
			},
			"Good case (registered afresh), exists different creation time in updated record",
		},
		{
			false,
			// Registered afresh since 30 August 2026: an id this registrar
			// holds for another record was issued elsewhere, before a failover.
			func(ua *Traits) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", time.Now().Add(1*time.Hour).Format(time.RFC3339), ch)
				return err
			},
			"Good case (registered afresh), mismatch between db- and received created field",
		},
		{
			false,
			func(ua *Traits) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				return err
			},
			"Good case, recCount has looped back to 0",
		},
		{
			false,
			func(ua *Traits) error {
				ch := ua.requests
				err := sendAddRequest(0, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				if err != nil {
					t.Fatalf("Failed sending first request")
				}
				err = sendAddRequest(1, "testDef", "subP", time.Now().Format(time.RFC3339), ch)
				return err
			},
			"Good case, updated db record",
		},
	}

	for _, c := range params {
		// Setup
		temp := createConfAssetMultipleTraits()
		sys := createNewSys()
		res, shutdown := newResource(temp, &sys)
		ua := res.Traits.(*Traits)

		// Test and check
		err := c.request(ua)

		if c.expectError == false && err != nil {
			t.Errorf("Expected no errors in '%s': %v", c.testCase, err)
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}
		shutdown()
	}
}

// --------------------------------------------------------------------------- //
// Help functions and structs to test the read part of serviceRegistryHandler()
// --------------------------------------------------------------------------- //

func sendAddRequestWithDetails(id int64, def string, subPath string, created string, ch chan ServiceRegistryRequest) error {
	rec := &forms.ServiceRecord_v1{
		Id:                int(id),
		ServiceDefinition: def,
		SystemName:        "System",
		ServiceNode:       "node",
		IPAddresses:       []string{"123.456.789.012"},
		ProtoPort:         map[string]int{"http": 1234},
		Details:           map[string][]string{"details": {}},
		Certificate:       "ABCD",
		SubPath:           subPath,
		RegLife:           25,
		Version:           "SignalA_v1a",
		Created:           created,
		Updated:           time.Now().String(),
		EndOfValidity:     time.Now().Add(25 * time.Second).String(),
		SubscribeAble:     false,
		ACost:             float64(id),
		CUnit:             "",
	}

	for x := range id {
		rec.Details["details"] = append(rec.Details["details"], fmt.Sprintf("detail%d", x+1))
	}

	req := ServiceRegistryRequest{
		Action: "add",
		Id:     0,
		Record: rec,
		Error:  make(chan error),
	}

	ch <- req
	if err := <-req.Error; err != nil {
		return err
	}

	return nil
}

// id 0 will return all items in service registry, any other will return items depending on details & definition
func sendReadRequest(id int64, def string, details []string, ch chan ServiceRegistryRequest) ([]forms.ServiceRecord_v1, error) {
	rec := &forms.ServiceQuest_v1{
		SysId:             999,
		RequesterName:     "requester",
		ServiceDefinition: def,
		Protocol:          "",
		Details:           map[string][]string{"details": details},
		Version:           "",
	}
	var req ServiceRegistryRequest
	if id == 0 {
		// Returns a specific
		req = ServiceRegistryRequest{
			Action: "read",
			Record: nil,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
	} else {
		// Returns full list of services
		req = ServiceRegistryRequest{
			Action: "read",
			Record: rec,
			Result: make(chan []forms.ServiceRecord_v1),
			Error:  make(chan error),
		}
	}

	ch <- req
	select {
	case err := <-req.Error:
		return nil, err
	case lst := <-req.Result:
		return lst, nil
	}
}

func sendBrokenReadRequest(ch chan ServiceRegistryRequest) ([]forms.ServiceRecord_v1, error) {
	rec := &forms.SignalA_v1a{}

	var req = ServiceRegistryRequest{
		Action: "read",
		Record: rec,
		Result: make(chan []forms.ServiceRecord_v1),
		Error:  make(chan error),
	}

	ch <- req
	select {
	case err := <-req.Error:
		return nil, err
	case lst := <-req.Result:
		return lst, nil
	}
}

type serviceRegistryHandlerReadParams struct {
	expectError bool
	expectedLen int
	request     func(ua *Traits) ([]forms.ServiceRecord_v1, error)
	testCase    string
}

func TestServiceRegistryHandlerRead(t *testing.T) {
	params := []serviceRegistryHandlerReadParams{
		{
			false,
			1,
			func(ua *Traits) ([]forms.ServiceRecord_v1, error) {
				return sendReadRequest(0, "", []string{""}, ua.requests)
			},
			"Best case, successful read request returning all items",
		},
		{
			false,
			1,
			func(ua *Traits) ([]forms.ServiceRecord_v1, error) {
				return sendReadRequest(1, "test", []string{"detail6"}, ua.requests)
			},
			"Best case, successful read request returning specific items",
		},
		{
			true,
			0,
			func(ua *Traits) ([]forms.ServiceRecord_v1, error) {
				return sendBrokenReadRequest(ua.requests)
			},
			"Bad case, wrong form",
		},
	}

	for _, c := range params {
		// Setup
		temp := createConfAssetMultipleTraits()
		sys := createNewSys()
		res, shutdown := newResource(temp, &sys)
		ua := res.Traits.(*Traits)
		time.Sleep(25 * time.Millisecond)
		// Add some services to the serviceregistrar with details: detail1 detail2 ... detailN
		sendAddRequestWithDetails(1, "test", "sub1", time.Now().Format(time.RFC3339), ua.requests)
		sendAddRequestWithDetails(4, "test", "sub2", time.Now().Format(time.RFC3339), ua.requests)
		sendAddRequestWithDetails(8, "test", "sub3", time.Now().Format(time.RFC3339), ua.requests)

		lst, err := c.request(ua)

		if c.expectError == false && err != nil && len(lst) != c.expectedLen {
			t.Errorf("Expected no errors in '%s', got: %v, with length of list: %d got %d",
				c.testCase, err, c.expectedLen, len(lst))
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}

		shutdown()
	}
}

// ------------------------------------------------------------------------ //
// Help functions and structs to test delete in serviceRegistryHandler()
// ------------------------------------------------------------------------ //

func sendDeleteRequest(id int, ch chan ServiceRegistryRequest) {
	ch <- ServiceRegistryRequest{
		Action: "delete",
		Id:     int64(id),
	}
}

func TestServiceRegistryHandlerDelete(t *testing.T) {
	// Setup
	temp := createConfAssetMultipleTraits()
	sys := createNewSys()
	res, shutdown := newResource(temp, &sys)
	ua := res.Traits.(*Traits)
	time.Sleep(25 * time.Millisecond)
	// Add a services to the serviceregistrar
	sendAddRequestWithDetails(1, "test", "sub1", time.Now().Format(time.RFC3339), ua.requests)

	sendDeleteRequest(0, ua.requests)

	shutdown()
}

// held wraps a record as the registry holds it, with a timer that will never
// fire — a test asserts on the map, not on the clock.
func held(rec forms.ServiceRecord_v1) *registration {
	return &registration{
		ServiceRecord_v1: rec,
		expires:          time.Now().Add(time.Hour),
		expiry:           time.AfterFunc(time.Hour, func() {}),
	}
}

// ------------------------------------------------------------------------ //
// Help functions and structs to test FilterRecords()
// ------------------------------------------------------------------------ //

// Creates an asset multiple services in its registry
func createRegistryWithServices(broken bool) (ua *Traits, err error) {
	ua = &Traits{serviceRegistry: make(map[int]*registration)}

	var locations = []string{"Kitchen", "Bathroom", "Livingroom"}

	for i, location := range locations {
		var form forms.ServiceRecord_v1
		form.ServiceDefinition = "testDef"
		form.SystemName = fmt.Sprintf("testSystem%d", i)
		form.ProtoPort = map[string]int{"http": i}
		form.IPAddresses = []string{fmt.Sprintf("999.999.%d.999", i)}
		form.EndOfValidity = "2026-01-02T15:04:05Z"
		form.Details = make(map[string][]string)
		if !broken {
			form.Details = map[string][]string{"Location": {location}}
		}
		ua.serviceRegistry[i] = held(form)
	}
	return ua, nil
}

type filterByServDefAndDetailsParams struct {
	expectMatch bool
	setup       func() (*Traits, error)
	testCase    string
}

func TestFilterByServiceDefAndDetails(t *testing.T) {
	params := []filterByServDefAndDetailsParams{
		{
			true,
			func() (ua *Traits, err error) { return createRegistryWithServices(false) },
			"Best case",
		},
		{
			false,
			func() (ua *Traits, err error) { return createRegistryWithServices(true) },
			"Bad case, key doesn't exist",
		},
	}

	for _, c := range params {
		ua, err := c.setup()
		if err != nil {
			t.Errorf("Failed during setup in '%s'", c.testCase)
		}
		checkLoc := map[string][]string{"Location": {"Livingroom"}}
		lst := ua.FilterRecords(forms.ServiceQuest_v1{
			ServiceDefinition: "testDef",
			Details:           checkLoc,
		})
		if (c.expectMatch == true) && (len(lst) < 1) {
			t.Errorf("Expected atleast 1 service")
		}
		if (c.expectMatch == false) && (len(lst) > 0) {
			t.Errorf("Expected no matches")
		}
	}
}

// ---------------------------------------------------- //
// Help functions and structs to test expire()
// ---------------------------------------------------- //

func createRegistryWithService(expires time.Time) (ua *Traits, cancel func(), err error) {
	sys := createNewSys()
	temp, cancel := newResource(createConfAssetMultipleTraits(), &sys)
	ua = temp.Traits.(*Traits)

	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"http": 1234}
	test.IPAddresses = []string{"999.999.999.999"}
	reg := held(test)
	reg.expires = expires
	ua.serviceRegistry = map[int]*registration{0: reg}
	return ua, cancel, err
}

type checkExpirationParams struct {
	servicePresent bool
	setup          func() (*Traits, func(), error)
	testCase       string
}

func TestCheckExpiration(t *testing.T) {
	params := []checkExpirationParams{
		{
			true,
			func() (ua *Traits, cancel func(), err error) {
				return createRegistryWithService(time.Now().Add(time.Hour))
			},
			"Best case, service not past expiration",
		},
		{
			false,
			func() (ua *Traits, cancel func(), err error) {
				return createRegistryWithService(time.Now().Add(-time.Hour))
			},
			"Bad case, service past expiration",
		},
		// There is no longer a "time parsing problem" case: the registry holds
		// the instant itself, so an unparseable expiry is not a state a record
		// can be in.
	}
	for _, c := range params {
		ua, cancel, err := c.setup()
		if err != nil {
			t.Errorf("failed during setup: %v", err)
		}

		ua.expire(0)
		if _, exists := ua.serviceRegistry[0]; (exists == false) && (c.servicePresent == true) {
			t.Errorf("expected the service to be present in '%s'", c.testCase)
		}
		if _, exists := ua.serviceRegistry[0]; (exists == true) && (c.servicePresent == false) {
			t.Errorf("expected service to be removed in '%s'", c.testCase)
		}

		cancel()
	}
}

// ----------------------------------------------------- //
// Help functions and structs to test getUniqueSystems()
// ----------------------------------------------------- //

func createServRegistryHttp() (ua *Traits, err error) {
	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"http": 1234}
	test.IPAddresses = []string{"999.999.999.999"}
	return &Traits{serviceRegistry: map[int]*registration{0: held(test)}}, nil
}

func createServRegistryHttps() (ua *Traits, err error) {
	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"https": 4321}
	test.IPAddresses = []string{"888.888.888.888"}
	return &Traits{serviceRegistry: map[int]*registration{0: held(test)}}, nil
}

func createBrokenServRegistry() (ua *Traits, err error) {
	var test forms.ServiceRecord_v1
	test.SystemName = "testSystem"
	test.ProtoPort = map[string]int{"https": 0}
	test.IPAddresses = []string{"888.888.888.888"}
	return &Traits{serviceRegistry: map[int]*registration{0: held(test)}}, nil
}

type getUniqueSystemsParams struct {
	expectError bool
	setup       func() (ua *Traits, err error)
	testCase    string
}

func TestGetUniqueSystems(t *testing.T) {
	params := []getUniqueSystemsParams{
		{
			false,
			func() (ua *Traits, err error) { return createServRegistryHttp() },
			"Best case, http",
		},
		{
			false,
			func() (ua *Traits, err error) { return createServRegistryHttps() },
			"Best case, https",
		},
		{
			false,
			func() (ua *Traits, err error) { return createBrokenServRegistry() },
			"Bad case, http/https not found",
		},
	}

	for _, c := range params {
		ua, err := c.setup()
		if err != nil {
			t.Errorf("Failed during setup in '%s' with error: %v", c.testCase, err)
		}
		_, err = getUniqueSystems(ua)
		if c.expectError == false && err != nil {
			t.Errorf("Failed while getting unique systems in '%s': %v", c.testCase, err)
		}
		if c.expectError == true && err == nil {
			t.Errorf("Expected errors in '%s'", c.testCase)
		}
	}
}

// ProviderName filters on whose records are wanted, which is how the authorizer
// reads a system's own attributes: it asks what that system provides rather than
// trusting anything the caller asserted about it.
func TestFilterRecordsByProvider(t *testing.T) {
	ua, err := createRegistryWithServices(false)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	tests := []struct {
		name  string
		quest forms.ServiceQuest_v1
		want  int
	}{
		{
			name:  "a provider on its own returns everything it provides",
			quest: forms.ServiceQuest_v1{ProviderName: "testSystem1"},
			want:  1,
		},
		{
			name:  "a definition on its own still returns every provider's",
			quest: forms.ServiceQuest_v1{ServiceDefinition: "testDef"},
			want:  3,
		},
		{
			name:  "both criteria narrow together",
			quest: forms.ServiceQuest_v1{ServiceDefinition: "testDef", ProviderName: "testSystem2"},
			want:  1,
		},
		{
			name:  "an unknown provider matches nothing",
			quest: forms.ServiceQuest_v1{ProviderName: "nosuchsystem"},
			want:  0,
		},
		{
			name:  "a mismatched definition still excludes a known provider",
			quest: forms.ServiceQuest_v1{ServiceDefinition: "otherDef", ProviderName: "testSystem0"},
			want:  0,
		},
		{
			name: "details narrow a provider query too",
			quest: forms.ServiceQuest_v1{
				ProviderName: "testSystem0",
				Details:      map[string][]string{"Location": {"Bathroom"}},
			},
			want: 0, // testSystem0 is in the Kitchen
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(ua.FilterRecords(tc.quest)); got != tc.want {
				t.Errorf("FilterRecords returned %d records; want %d", got, tc.want)
			}
		})
	}
}

// A quest narrowing nothing must not be answered with the whole registry: a
// typo would otherwise become a disclosure of every service in the cloud.
func TestFilterRecordsRefusesUnnarrowedQuests(t *testing.T) {
	ua, err := createRegistryWithServices(false)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	if got := ua.FilterRecords(forms.ServiceQuest_v1{}); len(got) != 0 {
		t.Errorf("an empty quest returned %d records; want none", len(got))
	}

	// Details alone do not count as narrowing: the guard is about naming either
	// what is wanted or whose it is.
	detailsOnly := forms.ServiceQuest_v1{Details: map[string][]string{"Location": {"Kitchen"}}}
	if got := ua.FilterRecords(detailsOnly); len(got) != 0 {
		t.Errorf("a details-only quest returned %d records; want none", len(got))
	}
}

// --------------------------------------------------------------------------- //
// What a subscriber is told about, and what it is not
// --------------------------------------------------------------------------- //

// subscribe attaches a subscriber to a running registry and returns it with the
// events it has received so far, read on demand.
func subscribe(t *testing.T, tr *Traits) (*subscriber, func() []forms.RegistryEvent_v1) {
	t.Helper()
	sub, remove := tr.addSubscriber()
	t.Cleanup(remove)

	return sub, func() []forms.RegistryEvent_v1 {
		// The registry notifies after releasing its lock, so give the send a
		// moment to land before deciding nothing arrived.
		time.Sleep(50 * time.Millisecond)
		var got []forms.RegistryEvent_v1
		for {
			select {
			case ev := <-sub.events:
				got = append(got, ev)
			default:
				return got
			}
		}
	}
}

// TestReregistrationIsNotAChange is the reason this exists: a service confirms
// it is still there every RegPeriod seconds, and the registry used to wake every
// subscriber for each one — more than once a second across a cloud of this size,
// every time with a list identical to the last.
func TestReregistrationIsNotAChange(t *testing.T) {
	temp := createConfAssetMultipleTraits()
	sys := createNewSys()
	res, shutdown := newResource(temp, &sys)
	defer shutdown()
	tr := res.Traits.(*Traits)

	_, collect := subscribe(t, tr)

	// A service registers for the first time.
	if err := sendAddRequest(0, "temperature", "sensor/temp", "", tr.requests); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	first := collect()
	if len(first) != 1 {
		t.Fatalf("a new registration produced %d events, want 1", len(first))
	}
	if first[0].Change != forms.RegistryRegistered {
		t.Errorf("change = %q, want %q", first[0].Change, forms.RegistryRegistered)
	}
	if first[0].Record.ServiceDefinition != "temperature" {
		t.Errorf("the event does not name the service: %+v", first[0].Record)
	}

	// The same service says it is still there, three times over.
	id := first[0].Record.Id
	created := first[0].Record.Created
	for i := 0; i < 3; i++ {
		if err := sendAddRequest(int64(id), "temperature", "sensor/temp", created, tr.requests); err != nil {
			t.Fatalf("re-registration %d: %v", i, err)
		}
	}

	if again := collect(); len(again) != 0 {
		t.Errorf("re-registration woke the subscriber %d times; it is a confirmation, not a change", len(again))
	}
}

// A deregistration is reported, and carries the record as it last stood so a
// subscriber can act on what left without having kept its own copy.
func TestDeregistrationIsReportedWithWhatLeft(t *testing.T) {
	temp := createConfAssetMultipleTraits()
	sys := createNewSys()
	res, shutdown := newResource(temp, &sys)
	defer shutdown()
	tr := res.Traits.(*Traits)

	_, collect := subscribe(t, tr)

	if err := sendAddRequest(0, "rotation", "servo/rotation", "", tr.requests); err != nil {
		t.Fatalf("registration: %v", err)
	}
	registered := collect()
	if len(registered) != 1 {
		t.Fatalf("registration produced %d events, want 1", len(registered))
	}
	id := registered[0].Record.Id

	req := ServiceRegistryRequest{Action: "delete", Id: int64(id), Error: make(chan error)}
	tr.requests <- req
	if err := <-req.Error; err != nil {
		t.Fatalf("deregistration: %v", err)
	}

	gone := collect()
	if len(gone) != 1 {
		t.Fatalf("deregistration produced %d events, want 1", len(gone))
	}
	if gone[0].Change != forms.RegistryDeregistered {
		t.Errorf("change = %q, want %q", gone[0].Change, forms.RegistryDeregistered)
	}
	if gone[0].Record.ServiceDefinition != "rotation" {
		t.Errorf("the event does not say what left: %+v", gone[0].Record)
	}
}

// TestTheRegistrarStreamsOnADeclaredService is about the difference between a
// path that answers and a service the cloud knows about.
//
// /syslist answered for a while without being declared. That works only in a
// cloud whose registrar has no authorizer to consult — where the framework lets
// an unknown path through — and returns 404 in one that does, which is the case
// the subscription exists for. Undeclared, it is also invisible to the
// Orchestrator, so a subscriber cannot be told where it is or be given a token
// to read it, and it appears nowhere in the knowledge graph.
//
// The configuration here has no services at all, which is what every registrar
// deployed before this service existed has: a template is written only when
// there is no systemconfig.json, and never merged into one that is already
// there. So the declaration alone would have left those registrars 404ing.
func TestTheRegistrarStreamsOnADeclaredService(t *testing.T) {
	sys := createTestSystem()
	ua, cleanup := newResource(createConfAssetMultipleTraits(), &sys)
	defer cleanup()

	serv, declared := ua.ServicesMap[systemListPath]
	if !declared {
		t.Fatalf("the registrar serves %q but does not declare it, so an authorized "+
			"cloud answers 404 there and the Orchestrator cannot find it", systemListPath)
	}
	if serv.Definition == "" {
		t.Error("the service has no definition, so the authorizer has no policy to apply to it")
	}
}

// A renewal that lands while the old timer has already fired must win.
//
// Stop returns false once the timer's function has started, so a renewal
// cannot rely on it; the function then arrives at expire holding a stale
// deadline. The record it finds is the renewed one, not yet due, and it must
// be left alone — otherwise a service that re-registered on time is deleted
// for the lateness of the message it had just superseded.
func TestRenewalOutlivesAFiredTimer(t *testing.T) {
	sys := createNewSys()
	temp, cancel := newResource(createConfAssetMultipleTraits(), &sys)
	defer cancel()
	ua := temp.Traits.(*Traits)

	var rec forms.ServiceRecord_v1
	rec.SystemName = "testSystem"
	rec.ServiceDefinition = "temperature"
	stale := held(rec)
	stale.expires = time.Now().Add(-time.Minute) // the timer that fired
	ua.serviceRegistry = map[int]*registration{7: stale}

	// The renewal replaces the entry before the fired timer reaches expire.
	renewed := held(rec)
	ua.mu.Lock()
	ua.serviceRegistry[7] = renewed
	ua.mu.Unlock()

	ua.expire(7) // the old timer's function, arriving late
	if _, present := ua.serviceRegistry[7]; !present {
		t.Fatal("a renewed registration was deleted by the timer it had replaced")
	}

	// And a registration that genuinely lapsed still goes.
	renewed.expires = time.Now().Add(-time.Second)
	ua.expire(7)
	if _, present := ua.serviceRegistry[7]; present {
		t.Fatal("a lapsed registration survived expire")
	}
}

// A verified system may remove only what it registered.
//
// The registry's services are core-mission and so exempt from tokens, which
// left any enrolled system free to delete any other's registration by
// guessing a number. An unverified caller is not checked — a cloud with no CA
// has nothing to check against — which is the case the httptest requests
// above exercise; this one supplies an owner.
func TestOnlyTheRegistrantMayDelete(t *testing.T) {
	sys := createNewSys()
	temp, cancel := newResource(createConfAssetMultipleTraits(), &sys)
	defer cancel()
	ua := temp.Traits.(*Traits)

	var rec forms.ServiceRecord_v1
	rec.SystemName = "thermostat"
	rec.ServiceDefinition = "setpoint"
	ua.mu.Lock()
	ua.serviceRegistry = map[int]*registration{5: held(rec)}
	ua.mu.Unlock()

	ask := func(owner string) error {
		req := ServiceRegistryRequest{Action: "delete", Id: 5, Owner: owner, Error: make(chan error)}
		ua.requests <- req
		return <-req.Error
	}

	if err := ask("collector"); !errors.Is(err, errNotOwner) {
		t.Fatalf("another system deleted the thermostat's registration: err=%v", err)
	}
	if _, present := ua.serviceRegistry[5]; !present {
		t.Fatal("the record was removed by a system that did not own it")
	}
	if err := ask("thermostat"); err != nil {
		t.Fatalf("the owner could not delete its own registration: %v", err)
	}
	if _, present := ua.serviceRegistry[5]; present {
		t.Fatal("the owner's delete did not remove the record")
	}
}

// After a failover, a system renews with the id the old lead gave it. The new
// lead may hold that number for someone else, and it must treat the renewal as
// the registration it never saw rather than refuse it.
func TestARenewalWithAForeignIdIsRegisteredAfresh(t *testing.T) {
	sys := createNewSys()
	temp, cancel := newResource(createConfAssetMultipleTraits(), &sys)
	defer cancel()
	ua := temp.Traits.(*Traits)

	other := forms.ServiceRecord_v1{SystemName: "parallax", ServiceDefinition: "rotation", SubPath: "Servo_1/rotation", Created: "2026-08-30T15:00:00Z", RegLife: 30}
	ua.mu.Lock()
	ua.serviceRegistry = map[int]*registration{13: held(other)}
	ua.mu.Unlock()

	renewal := &forms.ServiceRecord_v1{Id: 13, SystemName: "collector", ServiceDefinition: "mquery", SubPath: "demo/mquery", Created: "2026-08-30T14:00:00Z", RegLife: 30}
	req := ServiceRegistryRequest{Action: "add", Record: renewal, Error: make(chan error)}
	ua.requests <- req
	if err := <-req.Error; err != nil {
		t.Fatalf("a renewal with a foreign id was refused: %v", err)
	}
	if renewal.Id == 13 || renewal.Id == 0 {
		t.Fatalf("the renewal kept id %d instead of being registered afresh", renewal.Id)
	}
	if got := ua.serviceRegistry[13]; got == nil || got.SystemName != "parallax" {
		t.Fatal("the record that legitimately held id 13 was disturbed")
	}
}
