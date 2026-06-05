package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
)

func TestWriteWireguardStatus_ActiveWithMonitor(t *testing.T) {
	reresolveAgo := 305
	st := &companion.WireGuardStatusResponse{
		Tunnel:    "wg0",
		State:     "active",
		Interface: &companion.WireGuardIface{PublicKey: "PUB", ListeningPort: 57775},
		Peers: []companion.WireGuardPeer{{
			Endpoint:        "87.123.51.187:51826",
			LatestHandshake: "1m46s",
			TransferRx:      "1.23 KiB",
			TransferTx:      "4.56 KiB",
		}},
		Monitor: &companion.WireGuardMonitor{
			Running:              true,
			Hostnames:            []string{"pi.example.org"},
			Healthy:              true,
			Resolved:             map[string]string{"pi.example.org": "87.123.51.187"},
			LastReresolveSecsAgo: &reresolveAgo,
		},
	}
	var buf bytes.Buffer
	writeWireguardStatus(&buf, st)
	out := buf.String()

	for _, want := range []string{
		"wireguard wg0  active",
		"rx=1.23 KiB tx=4.56 KiB",
		"hs=1m46s",
		"monitor  running  hostnames=1",
		"last re-resolve  5m5s ago → 87.123.51.187",
		"state  healthy",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestWriteWireguardStatus_MonitorBackoff(t *testing.T) {
	nextRetry := 30
	st := &companion.WireGuardStatusResponse{
		Tunnel: "wg0",
		State:  "active",
		Peers:  []companion.WireGuardPeer{{Endpoint: "1.2.3.4:51820", LatestHandshake: "never"}},
		Monitor: &companion.WireGuardMonitor{
			Running:       true,
			Hostnames:     []string{"pi.example.org"},
			Healthy:       false,
			Attempt:       3,
			NextRetrySecs: &nextRetry,
			LastError:     "re-resolve failed for pi.example.org",
		},
	}
	var buf bytes.Buffer
	writeWireguardStatus(&buf, st)
	out := buf.String()

	for _, want := range []string{
		"state  reconnecting (attempt 3, next in 30s)",
		"last error  re-resolve failed for pi.example.org",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestWriteWireguardStatus_NoMonitor(t *testing.T) {
	st := &companion.WireGuardStatusResponse{Tunnel: "wg0", State: "active"}
	var buf bytes.Buffer
	writeWireguardStatus(&buf, st)
	if !strings.Contains(buf.String(), "monitor  not running") {
		t.Errorf("expected 'monitor not running', got:\n%s", buf.String())
	}
}

func TestWriteCompanionLogs(t *testing.T) {
	res := &companion.LogsResponse{Entries: []companion.LogEntry{
		{Ts: 1780696789, Level: "WARNING", Name: "companion.wg_monitor", Message: "stale handshake on wg0"},
	}}

	// Component filtered: name omitted.
	var filtered bytes.Buffer
	writeCompanionLogs(&filtered, res, true)
	if strings.Contains(filtered.String(), "wg_monitor") {
		t.Errorf("filtered output should omit logger name, got: %s", filtered.String())
	}
	if !strings.Contains(filtered.String(), "WARNING") || !strings.Contains(filtered.String(), "stale handshake on wg0") {
		t.Errorf("filtered output missing level/message, got: %s", filtered.String())
	}

	// Unfiltered: trimmed logger name shown.
	var unfiltered bytes.Buffer
	writeCompanionLogs(&unfiltered, res, false)
	if !strings.Contains(unfiltered.String(), "wg_monitor: stale handshake on wg0") {
		t.Errorf("unfiltered output missing trimmed name, got: %s", unfiltered.String())
	}
}

func TestWriteCompanionLogs_Empty(t *testing.T) {
	var buf bytes.Buffer
	writeCompanionLogs(&buf, &companion.LogsResponse{}, false)
	if !strings.Contains(buf.String(), "(no log entries)") {
		t.Errorf("expected empty marker, got: %s", buf.String())
	}
}
