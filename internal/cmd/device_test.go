package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestFilterDevices_NameMatchesNameByUser(t *testing.T) {
	devices := []haapi.DeviceRegistryEntry{
		{ID: "dev1", Name: "shellydimmer2-EC64C9C67219", NameByUser: "Gemütliches Licht"},
		{ID: "dev2", Name: "Schlafzimmerlicht"},
	}
	rc := &deviceRegistryContext{devices: devices}

	flagDeviceName = "Gemüt"
	defer func() { flagDeviceName = "" }()

	result := filterDevices(devices, rc)
	if len(result) != 1 {
		t.Fatalf("filterDevices matched %d devices, want 1", len(result))
	}
	if result[0].ID != "dev1" {
		t.Errorf("matched device = %q, want dev1", result[0].ID)
	}
}

func TestFilterDevices_NameFallsBackToRegistryName(t *testing.T) {
	devices := []haapi.DeviceRegistryEntry{
		{ID: "dev1", Name: "shellydimmer2-EC64C9C67219", NameByUser: "Gemütliches Licht"},
		{ID: "dev2", Name: "Schlafzimmerlicht"},
	}
	rc := &deviceRegistryContext{devices: devices}

	flagDeviceName = "Schlaf"
	defer func() { flagDeviceName = "" }()

	result := filterDevices(devices, rc)
	if len(result) != 1 {
		t.Fatalf("filterDevices matched %d devices, want 1", len(result))
	}
	if result[0].ID != "dev2" {
		t.Errorf("matched device = %q, want dev2", result[0].ID)
	}
}

func TestFilterDevices_NameNoMatch(t *testing.T) {
	devices := []haapi.DeviceRegistryEntry{
		{ID: "dev1", Name: "shellydimmer2-EC64C9C67219", NameByUser: "Gemütliches Licht"},
	}
	rc := &deviceRegistryContext{devices: devices}

	flagDeviceName = "nonexistent"
	defer func() { flagDeviceName = "" }()

	result := filterDevices(devices, rc)
	if len(result) != 0 {
		t.Fatalf("filterDevices matched %d devices, want 0", len(result))
	}
}

func TestDeviceUserFacingName(t *testing.T) {
	tests := []struct {
		name string
		d    haapi.DeviceRegistryEntry
		want string
	}{
		{"prefers name_by_user", haapi.DeviceRegistryEntry{Name: "tech-id", NameByUser: "Friendly Name"}, "Friendly Name"},
		{"falls back to name", haapi.DeviceRegistryEntry{Name: "tech-id"}, "tech-id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deviceUserFacingName(tt.d); got != tt.want {
				t.Errorf("deviceUserFacingName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDeviceMatchesPattern_CaseSensitive: device ls --pattern used to be the
// sole case-INsensitive outlier among the --pattern-supporting commands
// (ent ls --pattern is case-sensitive, and docs/manual.md documents
// case-sensitive substring/glob everywhere --pattern appears).
func TestDeviceMatchesPattern_CaseSensitive(t *testing.T) {
	// Neither ID nor Name contains a lowercase "heat" substring, so a
	// case-sensitive match must fail — only the earlier case-insensitive bug
	// would make it pass.
	d := haapi.DeviceRegistryEntry{ID: "dev1", Name: "Heat Pump"}

	if !deviceMatchesPattern(d, "Heat") {
		t.Errorf("deviceMatchesPattern(%+v, %q) = false, want true (exact case)", d, "Heat")
	}
	if deviceMatchesPattern(d, "heat") {
		t.Errorf("deviceMatchesPattern(%+v, %q) = true, want false (case must matter, like ent ls --pattern)", d, "heat")
	}
}

// TestDeviceHasLabel_SubstringMatchesEnt mirrors ent.go's
// TestFilterEntitiesByLabel_MatchesPerLabelNotJoinedString /
// TestLabelExistsInRegistry_AgreesWithFilter: device ls --label and ent ls
// --label must apply the identical matchingLabelIDs rule.
func TestDeviceHasLabel_SubstringMatchesEnt(t *testing.T) {
	d := haapi.DeviceRegistryEntry{ID: "dev_heat", Labels: []string{"heat_pump"}}
	rc := &deviceRegistryContext{
		labelByID: map[string]haapi.LabelEntry{
			"heat_pump": {LabelID: "heat_pump", Name: "Heat Pump"},
		},
	}

	if !deviceHasLabel(d, rc, "heat") {
		t.Errorf(`deviceHasLabel(%+v, "heat") = false, want true (substring of the label name)`, d)
	}
	if deviceHasLabel(d, rc, "cool") {
		t.Errorf(`deviceHasLabel(%+v, "cool") = true, want false`, d)
	}
}

// TestRegistryEntityAreaName_DeviceFallback is the site-4 unit regression:
// the `device show` entity table had its own hand-rolled area lookup, a
// second copy of the same missing-fallback bug fixed in label.go.
func TestRegistryEntityAreaName_DeviceFallback(t *testing.T) {
	rc := &deviceRegistryContext{
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
			"bedroom": {AreaID: "bedroom", Name: "Bedroom"},
		},
		deviceByID: map[string]haapi.DeviceRegistryEntry{
			"dev1": {ID: "dev1", AreaID: "kitchen"},
		},
	}

	inherited := haapi.EntityRegistryEntry{EntityID: "sensor.inherited", DeviceID: "dev1"}
	if got := registryEntityAreaName(inherited, rc); got != "Kitchen" {
		t.Errorf("registryEntityAreaName(inherited) = %q, want Kitchen (inherited from dev1)", got)
	}

	override := haapi.EntityRegistryEntry{EntityID: "sensor.override", DeviceID: "dev1", AreaID: "bedroom"}
	if got := registryEntityAreaName(override, rc); got != "Bedroom" {
		t.Errorf("registryEntityAreaName(override) = %q, want Bedroom (entity's own area beats its device's)", got)
	}

	orphan := haapi.EntityRegistryEntry{EntityID: "sensor.orphan"}
	if got := registryEntityAreaName(orphan, rc); got != "" {
		t.Errorf("registryEntityAreaName(orphan) = %q, want empty (no area, no device)", got)
	}
}
