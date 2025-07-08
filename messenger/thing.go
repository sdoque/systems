package main

import (
	"encoding/json"
	"fmt"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

type Traits struct {
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

var _ components.UnitAsset = &UnitAsset{}

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
