package main

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/forms"
	"github.com/sdoque/mbaigo/usecases"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sys := components.NewSystem("messenger", ctx)
	sys.Husk = &components.Husk{
		Description: "is a logging system that recieves log messages from other systems.",
		Details:     map[string][]string{"Developer": {"alex"}},
		ProtoPort:   map[string]int{"https": 0, "http": 20106, "coap": 0},
		InfoLink:    "https://github.com/sdoque/systems/tree/main/messenger",
		DName: pkix.Name{
			CommonName:         sys.Name,
			Organization:       []string{"alex"},
			OrganizationalUnit: []string{"Systems"},
			Locality:           []string{"Luleå"},
			Province:           []string{"Norrbotten"},
			Country:            []string{"SE"},
		},
	}

	assetTemplate := initTemplate()
	sys.UAssets[assetTemplate.GetName()] = &assetTemplate
	rawResources, err := usecases.Configure(&sys)
	if err != nil {
		usecases.LogWarn(&sys, "configuration error: %v", err)
		return
	}

	sys.UAssets = make(map[string]*components.UnitAsset)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			usecases.LogError(&sys, "resource configuration error: %+v", err)
			return
		}
		ua, cleanup, err := newResource(uac, &sys)
		if err != nil {
			usecases.LogError(&sys, "new resource: %v", err)
			return
		}
		defer cleanup()
		sys.UAssets[ua.GetName()] = &ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)
	<-sys.Sigs
	usecases.LogInfo(&sys, "shutting down %s", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	case "message":
		ua.handleNewMessage(w, r)
	case "dashboard":
		ua.handleDashboard(w, r)
	default:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	}
}

func (ua *UnitAsset) handleNewMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	form, err := usecases.Unpack(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	msg, ok := form.(*forms.SystemMessage_v1)
	if !ok {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ua.addMessage(*msg) // Don't want to have to deal with pointers, hence the *
}

// Encapsulates the regular bytes.Buffer, in order to allow causing mock errors
type mockableBuffer struct {
	bytes.Buffer // This embedded struct is available as "mockableBuffer.Buffer" by default
	errWrite     error
}

func (mock *mockableBuffer) setWriteError(err error) {
	mock.errWrite = err
}

func (mock *mockableBuffer) Write(body []byte) (int, error) {
	if mock.errWrite != nil {
		return 0, mock.errWrite
	}
	return mock.Buffer.Write(body)
}

const testBufferHeader string = "x-testing-buffer"

func (ua *UnitAsset) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	errors, warnings, latest := ua.filterLogs()
	data := map[string]any{
		"Errors":   errors,
		"Warnings": warnings,
		"Latest":   latest,
	}

	buf := &mockableBuffer{}
	// Protects the special test header by enabling it's use only while running `go test`
	if testing.Testing() && r.Header.Get(testBufferHeader) != "" {
		// This write error will cause an error in the template.Execute() below
		buf.setWriteError(fmt.Errorf("mock error"))
	}
	if err := ua.tmplDashboard.Execute(buf, data); err != nil {
		usecases.LogError(ua.Owner, "execute dashboard: %s", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w) // Ignoring errors, can't do much with them anyways if the transfer fails
}
