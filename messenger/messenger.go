package main

import (
	"context"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"time"

	"github.com/sdoque/mbaigo/components"
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
		usecases.LogInfo(&sys, "configuration error: %v", err)
	}

	sys.UAssets = make(map[string]*components.UnitAsset)
	for _, raw := range rawResources {
		var uac usecases.ConfigurableAsset
		if err := json.Unmarshal(raw, &uac); err != nil {
			usecases.LogError(&sys, "resource configuration error: %+v", err)
		}
		ua, cleanup, err := newResource(uac, &sys)
		if err != nil {
			usecases.LogError(&sys, "new resource: %v", err)
		}
		defer cleanup()
		sys.UAssets[ua.GetName()] = &ua
	}

	usecases.RequestCertificate(&sys)
	usecases.RegisterServices(&sys)
	go usecases.SetoutServers(&sys)
	<-sys.Sigs
	usecases.LogInfo(&sys, "shuting down %s", sys.Name)
	cancel()
	time.Sleep(2 * time.Second)
}

func (ua *UnitAsset) Serving(w http.ResponseWriter, r *http.Request, servicePath string) {
	switch servicePath {
	default:
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
	}
}
