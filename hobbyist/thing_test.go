package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sdoque/mbaigo/components"
	"github.com/sdoque/mbaigo/usecases"
)

// The Re 421 pair, as the Central Station would report them.
func theSet() []Locomotive {
	return []Locomotive{
		{UID: 0x4001, Name: "421 393-0", Kind: DecoderMFX, Functions: []Function{
			{Number: 0, Name: "Light"}, {Number: 3, Name: "Horn"}, {Number: 4, Name: ""},
		}},
		{UID: 0x4002, Name: "421 387-2", Kind: DecoderMFX, Functions: []Function{
			{Number: 0, Name: "Light"},
		}},
	}
}

type fixedSource struct{ locos []Locomotive }

func (f fixedSource) Locomotives() ([]Locomotive, error) { return f.locos, nil }

// Each locomotive becomes one unit asset, and each of its functions a service —
// which is what makes a horn something the cloud can be asked to sound.
func TestOneAssetPerLocomotiveWithAServicePerFunction(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()

	if len(assets) != 2 {
		t.Fatalf("got %d assets; want one per locomotive", len(assets))
	}

	first := assets[0]
	if first.GetName() != "421_393-0" {
		t.Errorf("asset name = %q; want the station's name in a URL", first.GetName())
	}

	// Speed and direction, plus one service per function.
	want := []string{"speed", "direction", "light", "horn", "f4"}
	for _, subpath := range want {
		if _, ok := first.GetServices()[subpath]; !ok {
			t.Errorf("no %q service; got %v", subpath, subpaths(first))
		}
	}
	if len(first.GetServices()) != len(want) {
		t.Errorf("services = %v; want exactly %v", subpaths(first), want)
	}
}

// A locomotive moves, so no place on the layout describes it — but its horn is
// on that engine, and a consumer paired to one locomotive should reach that
// one's horn and no other's.
func TestTheFunctionalLocationIsTheLocomotive(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()

	for i, ua := range assets {
		got := ua.GetDetails()["FunctionalLocation"]
		want := theSet()[i].Name
		if len(got) != 1 || got[0] != want {
			t.Errorf("FunctionalLocation = %v; want %q", got, want)
		}
	}
}

// Speed on the wire is a fraction of the decoder's full scale, so it is a ratio
// with its range stated — 50 means nothing without knowing what it is half of.
func TestSpeedIsARatioWithItsRangeStated(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()

	speed := assets[0].GetServices()["speed"]
	if speed == nil {
		t.Fatal("no speed service")
	}
	if unit := speed.Details["Unit"]; len(unit) != 1 || unit[0] != "<http://qudt.org/vocab/unit/PERCENT>" {
		t.Errorf("Unit = %v; want percent", unit)
	}
	if kind := speed.Details["QuantityKind"]; len(kind) != 1 {
		t.Errorf("QuantityKind = %v; without one no consumer finds it by what it is", kind)
	}
	if r := speed.Details["Range"]; len(r) != 2 || r[1] != "1000" {
		t.Errorf("Range = %v; want the decoder's full scale", r)
	}
	if speed.Mission != "actuation" {
		t.Errorf("mission = %q; commanding a locomotive acts on the world", speed.Mission)
	}
}

// A locomotive in its box is still one the station knows: its services exist and
// say so, rather than vanishing from the registry every time an engine is lifted
// off the rails.
func TestServicesRefuseWhileTheLocomotiveIsOffTheTrack(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()

	tr, ok := assets[0].GetTraits().(*Traits)
	if !ok {
		t.Fatalf("traits are %T", assets[0].GetTraits())
	}
	if tr.available() {
		t.Error("a locomotive nobody has seen was reported as available")
	}

	// The layout reporting anything about it is what makes it present.
	tr.Observe(FunctionFrame(0x4001, 0, true))
	if !tr.available() {
		t.Error("a locomotive the layout reported on is still not available")
	}

	// And a frame about another engine says nothing about this one.
	other, _ := assets[1].GetTraits().(*Traits)
	if other.available() {
		t.Error("one locomotive's frame marked another as present")
	}
}

// What the layout reports is what the services read back, so a consumer sees the
// speed the engine is running at rather than the last one commanded.
func TestObservedStateIsWhatIsServed(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()
	tr := assets[0].GetTraits().(*Traits)

	speed, err := SpeedFrame(0x4001, 40)
	if err != nil {
		t.Fatalf("SpeedFrame: %v", err)
	}
	tr.Observe(speed)
	tr.Observe(DirectionFrame(0x4001, false))
	tr.Observe(FunctionFrame(0x4001, 3, true))

	if got := tr.signalA(tr.speed, "").Value; got < 39.9 || got > 40.1 {
		t.Errorf("speed = %v; want 40", got)
	}
	if tr.forward {
		t.Error("direction was not observed")
	}
	if !tr.functions[3] {
		t.Error("the horn's state was not observed")
	}
}

// A command that goes nowhere must not look like one that arrived.
func TestCommandsFailWithoutABus(t *testing.T) {
	assets, cleanup := newResources(configurable(), nil, fixedSource{theSet()}, silentBus{})
	defer cleanup()
	tr := assets[0].GetTraits().(*Traits)

	if err := tr.setSpeed(50); err == nil {
		t.Error("a speed command succeeded with no connection to the layout")
	}
	if err := tr.setFunction(3, true); err == nil {
		t.Error("a horn command succeeded with no connection to the layout")
	}
}

func TestFunctionNaming(t *testing.T) {
	tests := []struct {
		in   Function
		want string
	}{
		{Function{Number: 0, Name: "Light"}, "light"},
		{Function{Number: 3, Name: "Horn"}, "horn"},
		{Function{Number: 5, Name: "Smoke generator"}, "smoke_generator"},
		{Function{Number: 6, Name: "Sound: Doors"}, "sound_doors"},
		{Function{Number: 4, Name: ""}, "f4"},
		{Function{Number: 7, Name: "   "}, "f7"},
	}
	for _, tc := range tests {
		if got := functionName(tc.in); got != tc.want {
			t.Errorf("functionName(%q) = %q; want %q", tc.in.Name, got, tc.want)
		}
	}
}

func TestLocoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "locomotives.json")
	if err := os.WriteFile(path, []byte(`[
	  {"uid": "0x4001", "name": "421 393-0", "functions": {"0": "Light", "3": "Horn"}},
	  {"uid": "4002",   "name": "421 387-2", "functions": {"f0": "Light"}}
	]`), 0o600); err != nil {
		t.Fatalf("writing the list: %v", err)
	}

	locos, err := LocoFile{Path: path}.Locomotives()
	if err != nil {
		t.Fatalf("Locomotives: %v", err)
	}
	if len(locos) != 2 {
		t.Fatalf("read %d locomotives; want 2", len(locos))
	}
	if locos[0].UID != 0x4001 || locos[1].UID != 0x4002 {
		t.Errorf("uids = %#x, %#x", locos[0].UID, locos[1].UID)
	}
	if locos[0].Kind != DecoderMFX {
		t.Errorf("kind = %q; an mfx address should be recognised as one", locos[0].Kind)
	}
	if len(locos[0].Functions) != 2 {
		t.Errorf("functions = %v", locos[0].Functions)
	}
}

func TestLocoFileRejectsWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"no uid":       `[{"name": "421 393-0"}]`,
		"uid not hex":  `[{"uid": "wagon", "name": "421 393-0"}]`,
		"no name":      `[{"uid": "0x4001"}]`,
		"function key": `[{"uid": "0x4001", "name": "x", "functions": {"horn": "Horn"}}]`,
		"not a list":   `{"uid": "0x4001"}`,
	} {
		path := filepath.Join(dir, "x.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		if _, err := (LocoFile{Path: path}).Locomotives(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// ---- helpers ----

func configurable() usecases.ConfigurableAsset { return usecases.ConfigurableAsset{} }

func subpaths(ua *components.UnitAsset) []string {
	var out []string
	for k := range ua.GetServices() {
		out = append(out, k)
	}
	return out
}
