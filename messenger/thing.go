package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

type message struct {
	time   time.Time
	level  forms.MessageLevel
	system string
	body   string
}

func (m message) String() string {
	return fmt.Sprintf("%s - %s - %s: %s",
		m.system,
		m.time.Format("2006-01-02 15:04:05"),
		forms.LevelToString(m.level),
		m.body,
	)
}

type UnitAsset struct {
	Name        string              `json:"name"`
	Owner       *components.System  `json:"-"`
	Details     map[string][]string `json:"details"`
	ServicesMap components.Services `json:"-"`
	CervicesMap components.Cervices `json:"-"`

	cachedRegMsg  []byte               // Caches the MessengerRegistration form
	messages      map[string][]message // Per system msg log
	mutex         sync.RWMutex         // Protects concurrent access to previous field
	tmplDashboard *template.Template   // The HTML template loaded from file
}

func (ua *UnitAsset) GetName() string { return ua.Name }

func (ua *UnitAsset) GetServices() components.Services { return ua.ServicesMap }

func (ua *UnitAsset) GetCervices() components.Cervices { return ua.CervicesMap }

func (ua *UnitAsset) GetDetails() map[string][]string { return ua.Details }

var _ components.UnitAsset = (*UnitAsset)(nil)

func initTemplate() components.UnitAsset {
	service := components.Service{
		Definition:  "message",
		SubPath:     "message",
		Details:     map[string][]string{"Forms": {"SystemMessage_v1"}},
		RegPeriod:   30,
		Description: "stores a new message in the log database",
	}
	return &UnitAsset{
		Name:        "log",
		Details:     map[string][]string{},
		ServicesMap: components.Services{service.SubPath: &service},
	}
}

// Instructs the compiler to load and embed the following file into the built binary

//go:embed dashboard.html
var tmplDashboard string

func newResource(ca usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func(), error) {
	ua := &UnitAsset{
		Name:        ca.Name,
		Owner:       sys,
		Details:     ca.Details,
		ServicesMap: usecases.MakeServiceMap(ca.Services),
		messages:    make(map[string][]message),
	}

	var err error
	ua.tmplDashboard, err = template.New("dashboard").Parse(tmplDashboard)
	if err != nil {
		return nil, nil, err
	}
	ua.cachedRegMsg, err = newRegMsg(sys)
	if err != nil {
		return nil, nil, err
	}
	go ua.runBeacon()
	f := func() {}
	return ua, f, nil
}

////////////////////////////////////////////////////////////////////////////////

// newRegMsg creates a new MessengerRegistration form filled with the system's URL.
// The form is then packed and cached, to be sent periodically by the beacon function.
func newRegMsg(sys *components.System) ([]byte, error) {
	// This system URL is created in the same way as the registrar,
	// in getUniqueSystems(). Using url.URL instead for safer string assembly.
	// https://github.com/lmas/mbaigo_systems/blob/dev/esr/thing.go#L404-L407
	var systemURL url.URL
	systemURL.Host = sys.Host.IPAddresses[0]
	systemURL.Scheme = "https"
	port := sys.Husk.ProtoPort[systemURL.Scheme]
	if port == 0 {
		systemURL.Scheme = "http"
		port = sys.Husk.ProtoPort[systemURL.Scheme]
		if port == 0 {
			return nil, fmt.Errorf("no http(s) port defined in conf")
		}
	}
	systemURL.Host += ":" + strconv.Itoa(port)
	systemURL.Path = sys.Name
	registration := forms.NewMessengerRegistration_v1(systemURL.String())
	return usecases.Pack(forms.Form(&registration), "application/json")
}

const beaconPeriod int = 30

// runBeacon runs periodically in the background (in a goroutine at startup).
// It fetches a list of systems and then sends out a MessengerRegistration to each.
func (ua *UnitAsset) runBeacon() {
	for {
		systems, err := ua.fetchSystems()
		if err != nil {
			usecases.LogInfo(ua.Owner, "error fetching system list: %s", err)
		}
		ua.notifySystems(systems)
		select {
		case <-time.Tick(time.Duration(beaconPeriod) * time.Second):
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
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("bad response: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// fetchSystems asks the registrar for a list of online systems.
func (ua *UnitAsset) fetchSystems() (systems []string, err error) {
	url, err := components.GetRunningCoreSystemURL(
		ua.Owner, components.ServiceRegistrarName)
	if err != nil {
		return
	}
	body, err := sendRequest("GET", url+"/syslist", nil)
	if err != nil {
		return
	}
	form, err := usecases.Unpack(body, "application/json")
	if err != nil {
		return
	}
	records, ok := form.(*forms.SystemRecordList_v1)
	if !ok {
		err = fmt.Errorf("form is not a SystemRecordList_v1")
		return
	}
	return records.List, nil
}

// notifySystems sends a pre-packed MessengerRegistration form to a list of online systems.
// Any systems with incorrect URLs, any messengers, and any http errors will be ignored.
func (ua *UnitAsset) notifySystems(list []string) {
	for _, sys := range list {
		sysURL, err := url.Parse(sys)
		if err != nil {
			continue // Skip misconfigured systems
		}
		if strings.HasPrefix(sysURL.Path, "/"+ua.Owner.Name) {
			continue // Skip itself and other messengers
		}
		// Don't care about any errors or any systems that don't want to talk with us
		// (using empty variable names to shut up the linter warning about unhandled errors)
		_, _ = sendRequest("POST", sys+"/msg", ua.cachedRegMsg)
	}
}

const maxMessages int = 10

// addMessage adds the new message m to a system's log and optionally removes the
// oldest, if the log's size is larger than maxMessages.
// Note that this function sets the timestamp of the incoming msg too.
func (ua *UnitAsset) addMessage(msg forms.SystemMessage_v1) {
	ua.mutex.Lock()
	defer ua.mutex.Unlock()
	ua.messages[msg.System] = append(ua.messages[msg.System], message{
		time:   time.Now(),
		level:  msg.Level,
		system: msg.System,
		body:   msg.Body,
	})
	if len(ua.messages[msg.System]) > maxMessages {
		// Strips the oldest msg from the front of the slice
		ua.messages[msg.System] = ua.messages[msg.System][1:]
	}
}

// filterLogs fetches the latest errors/warnings/all messages from the log.
// The log is appended to in a chronological order already, so the latest error
// and warning for each system will be returned and "all" will be in reverse
// chronological order.
// NOTE: No tests are provided for this function, as it's most likely subject
// to later changes.
func (ua *UnitAsset) filterLogs() (errors, warnings map[string]message, all []message) {
	errors = make(map[string]message)
	warnings = make(map[string]message)
	ua.mutex.RLock()
	for system := range ua.messages {
		for _, msg := range ua.messages[system] {
			all = append(all, msg)
			switch msg.level {
			case forms.LevelError:
				errors[system] = msg
			case forms.LevelWarn:
				warnings[system] = msg
			}
		}
	}
	ua.mutex.RUnlock()
	// Reverse order
	sort.Slice(all, func(i, j int) bool {
		return all[i].time.After(all[j].time)
	})
	return
}
