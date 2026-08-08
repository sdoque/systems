package main

import (
	"math"
	"testing"

	"github.com/sdoque/mbaigo/usecases"
)

// The chip reports millidegrees Celsius whatever the configuration says, so the
// unit a consumer sees is produced by conversion rather than by relabeling.
// If this were a label change, the number would be wrong by 32 degrees and no
// test anywhere would notice.
func TestReportsFahrenheitFromACelsiusSensor(t *testing.T) {
	from := celsius()
	to, ok := usecases.LookupUnit("http://qudt.org/vocab/unit/DEG_F")
	if !ok {
		t.Fatal("the QUDT table has no degree Fahrenheit")
	}

	for _, tc := range []struct{ chip, want float64 }{
		{0, 32}, {100, 212}, {21.5, 70.7}, {-40, -40},
	} {
		got, err := usecases.Convert(tc.chip, from, to, false)
		if err != nil {
			t.Fatalf("Convert: %v", err)
		}
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%.1f degrees Celsius reported as %.4f; want %.4f", tc.chip, got, tc.want)
		}
	}
}

// The template must declare a unit the framework can convert into, or the system
// refuses to start — which is the intended behavior, but only useful if the
// shipped template is not itself the thing that fails.
func TestTemplateDeclaresAConvertibleUnit(t *testing.T) {
	ua := initTemplate()

	declared := ua.GetDetails()["Unit"]
	if len(declared) != 1 {
		t.Fatalf("the template declares %v units; want exactly one", declared)
	}
	unit, ok := usecases.LookupUnit(declared[0])
	if !ok {
		t.Fatalf("the template declares %q, which is not a known QUDT unit", declared[0])
	}
	if _, err := usecases.Convert(0, celsius(), unit, false); err != nil {
		t.Errorf("the template's unit cannot be produced from the sensor: %v", err)
	}

	// And it must say what kind of quantity it is, or no consumer asking for a
	// temperature will ever be paired with it.
	if kind := ua.GetDetails()["QuantityKind"]; len(kind) != 1 {
		t.Errorf("QuantityKind = %v; a provider that does not declare one is unfindable", kind)
	}
}
