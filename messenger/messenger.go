package main

import (
	"bytes"
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"net/http"
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
	b, err := io.ReadAll(r.Body)
	if err != nil {
		usecases.LogError(ua.Owner, "read request body: %v", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError),
			http.StatusInternalServerError)
		return
	}
	defer r.Body.Close()

	f, err := usecases.Unpack(b, r.Header.Get("Content-Type"))
	if err != nil {
		usecases.LogWarn(ua.Owner, "unpack: %v", err)
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	msg, ok := f.(*forms.SystemMessage_v1)
	if !ok {
		usecases.LogWarn(ua.Owner, "form is not a SystemMessage_v1")
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	ua.addMessage(*msg) // Don't want to have to deal with pointers, hence the *
}

func (ua *UnitAsset) handleDashboard(w http.ResponseWriter, r *http.Request) {
	errors, warnings, latest := ua.filterLogs()
	data := map[string]any{
		"Errors":   errors,
		"Warnings": warnings,
		"Latest":   latest,
	}

	buf := &bytes.Buffer{}
	if err := ua.tmplDashboard.Execute(buf, data); err != nil {
		usecases.LogError(ua.Owner, "execute dashboard: %s", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	buf.WriteTo(w) // Ignoring errors, can't do much with them anyways if the transfer fails
}
