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
