package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

type Traits struct {
	regMsg  []byte   // Cached MessengerRegistration form
	systems []string // All systems this messenger is registered with
}

type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`
	Traits
}

// TODO: check if pointer is necessary??
func (ua *UnitAsset) GetName() string { return ua.Name }

func (ua *UnitAsset) GetServices() components.Services { return ua.ServicesMap }

func (ua *UnitAsset) GetCervices() components.Cervices { return ua.CervicesMap }

func (ua *UnitAsset) GetDetails() map[string][]string { return ua.Details }

func (ua *UnitAsset) GetTraits() any { return ua.Traits }

var _ components.UnitAsset = (*UnitAsset)(nil)

func initTemplate() components.UnitAsset {
	s := components.Service{
		Definition:  "message",
		SubPath:     "message",
		Details:     map[string][]string{"Forms": {"SystemMessage_v1"}},
		RegPeriod:   30,
		Description: "stores a new message in the log database",
	}
	return &UnitAsset{
		Name:        "log",
		Details:     map[string][]string{},
		ServicesMap: components.Services{s.SubPath: &s},
	}
}

func newResource(ca usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func(), error) {
	ua := &UnitAsset{
		Name:        ca.Name,
		Owner:       sys,
		Details:     ca.Details,
		ServicesMap: usecases.MakeServiceMap(ca.Services),
	}
	traits, err := unmarshalTraits(ca.Traits)
	if err != nil {
		return nil, nil, err
	}
	ua.Traits = traits[0]
	ua.regMsg, err = newRegMsg(sys)
	if err != nil {
		return nil, nil, err
	}
	go ua.runBeacon()
	f := func() {}
	return ua, f, nil
}

func unmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {
	var traitsList []Traits
	for _, raw := range rawTraits {
		var t Traits
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("unmarshal trait: %w", err)
		}
		traitsList = append(traitsList, t)
	}
	return traitsList, nil
}

////////////////////////////////////////////////////////////////////////////////

// newRegMsg creates a new MessengerRegistration form filled with the system's URL.
// The form is then packed and cached, to be sent periodically by the beacon function.
func newRegMsg(sys *components.System) ([]byte, error) {
	// This system URL is created in the same way as the registrar,
	// in getUniqueSystems(). Using url.URL instead for safer string assembly.
	// https://github.com/lmas/mbaigo_systems/blob/dev/esr/thing.go#L404-L407
	var u url.URL
	u.Host = sys.Host.IPAddresses[0]
	u.Scheme = "https"
	port := sys.Husk.ProtoPort[u.Scheme]
	if port == 0 {
		u.Scheme = "http"
		port = sys.Husk.ProtoPort[u.Scheme]
		if port == 0 {
			return nil, fmt.Errorf("no http(s) port defined in conf")
		}
	}
	u.Host += ":" + strconv.Itoa(port)
	u.Path = sys.Name
	m := forms.NewMessengerRegistration_v1(u.String())
	return usecases.Pack(forms.Form(&m), "application/json")
}

const timeoutUpdate int = 60

// runBeacon runs periodically in the background (in a goroutine at startup).
// It fetches a list of systems and then sends out a MessengerRegistration to each.
func (ua *UnitAsset) runBeacon() {
	for {
		s, err := ua.fetchSystems()
		if err != nil {
			usecases.LogWarn(ua.Owner, "error fetching system list: %s", err)
		}
		ua.notifySystems(s)
		select {
		case <-time.Tick(time.Duration(timeoutUpdate) * time.Second):
		case <-ua.Owner.Ctx.Done():
			return
		}
	}
}

// sendRequest is a helper for sending json web requests.
// It returns either error or the response body as a byte array.
func sendRequest(method, url string, body []byte) ([]byte, error) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("bad response: %s", resp.Status)
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// fetchSystems asks the registrar for a list of online systems.
func (ua *UnitAsset) fetchSystems() (systems []string, err error) {
	url, err := components.GetRunningCoreSystemURL(
		ua.Owner, components.ServiceRegistrarName)
	if err != nil {
		return
	}
	b, err := sendRequest("GET", url+"/syslist", nil)
	f, err := usecases.Unpack(b, "application/json")
	if err != nil {
		return
	}
	list, ok := f.(*forms.SystemRecordList_v1)
	if !ok {
		err = fmt.Errorf("form is not a SystemRecordList_v1")
		return
	}
	return list.List, nil
}

// notifySystems sends a pre-packed MessengerRegistration form to a list of online systems.
// Any systems with incorrect URLs, any messengers, and any http errors will be ignored.
func (ua *UnitAsset) notifySystems(list []string) {
	for _, sys := range list {
		u, err := url.Parse(sys)
		if err != nil {
			continue // Skip misconfigured systems
		}
		if strings.HasPrefix(u.Path, "/"+ua.Owner.Name) {
			continue // Skip itself and other messengers
		}
		// Don't care about any errors or any systems that don't want to talk with us
		_, _ = sendRequest("POST", sys+"/msg", ua.regMsg)
	}
}
