package main

import "testing"

// ----------------------------------------------------- //
// Help functions and structs to tests initTemplate()
// ----------------------------------------------------- //

func TestInitTemplate(t *testing.T) {
	expectedServices := []string{"register", "query", "unregister", "status"}

	ua := initTemplate()
	services := ua.GetServices()

	// Check if expected name and services are present
	if ua.GetName() != "registry" {
		t.Errorf("Name mismatch expected 'registry', got: %s", ua.GetName())
	}

	for _, s := range expectedServices {
		if _, ok := services[s]; !ok {
			t.Errorf("Expected service '%s' to be present", s)
		}
	}
}

// --------------------------------------------- //
// Help functions and structs to test ***
// --------------------------------------------- //

// newResource(configuredAsset usecases.ConfigurableAsset, sys *components.System) (components.UnitAsset, func()) {...}
func TestNewResource(t *testing.T) {}

// UnmarshalTraits(rawTraits []json.RawMessage) ([]Traits, error) {}
func TestUnmarshalTraits(t *testing.T) {}

// (ua *UnitAsset) serviceRegistryHandler() {}
func TestServiceRegistryHandler(t *testing.T) {}

// (ua *UnitAsset) FilterByServiceDefinitionAndDetails(desiredDefinition string, requiredDetails map[string][]string) []forms.ServiceRecord_v1 {
func TestFilterByServiceDefAndDetails(t *testing.T) {}

// checkExpiration(ua *UnitAsset, servId int) {}
func TestCheckExpiration(t *testing.T) {
	t.Errorf("Next test in line")
}

// getUniqueSystems(ua *UnitAsset) (*forms.SystemRecordList_v1, error) {}
func TestGetUniqueSystems(t *testing.T) {}
