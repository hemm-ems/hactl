package haapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// TestRegistryListBlankIdentityErrorPointsAtRecovery — the message a stranded
// caller reads.
//
// One area with a blank `area_id` fails EVERY `hactl area` command, by design:
// H-14 refuses to render a record that decoded without its identity, and
// `area delete` has to list the registry before it can resolve anything. That
// design is right — a listing quietly missing a row is the disease the poison
// exists to prevent — but it left the user with an error that explains the
// decode and stops there. In the live-fire report the operator had to work out
// on their own that a raw WebSocket delete was the only way back, while every
// `area` command in the session kept failing.
//
// So the diagnosis stays and a way out is added: the error names the blank
// record as a possible cause and says what removes it. The wrapping must keep
// both the marker (the integration harness greps command errors for it) and
// degeneracy.ErrDegenerate (callers distinguish "unavailable" from "wrong
// shape" with errors.Is).
func TestRegistryListBlankIdentityErrorPointsAtRecovery(t *testing.T) {
	cases := []struct {
		list    func(*WSClient) error
		kind    string
		wsCmd   string
		idField string
		payload []map[string]any
	}{
		{
			kind: "area", wsCmd: "config/area_registry/delete", idField: "area_id",
			payload: []map[string]any{{"area_id": "kitchen", "name": "Kitchen"}, {"area_id": "", "name": ""}},
			list:    func(ws *WSClient) error { _, err := ws.AreaRegistryList(context.Background()); return err },
		},
		{
			kind: "floor", wsCmd: "config/floor_registry/delete", idField: "floor_id",
			payload: []map[string]any{{"floor_id": "ground", "name": "Ground"}, {"floor_id": "", "name": ""}},
			list:    func(ws *WSClient) error { _, err := ws.FloorRegistryList(context.Background()); return err },
		},
		{
			kind: "label", wsCmd: "config/label_registry/delete", idField: "label_id",
			payload: []map[string]any{{"label_id": "energy", "name": "Energy"}, {"label_id": "", "name": ""}},
			list:    func(ws *WSClient) error { _, err := ws.LabelRegistryList(context.Background()); return err },
		},
	}

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
				sendWSResult(t, c, cmd, tc.payload)
			})
			defer srv.Close()
			ws := connectWSTest(t, srv)
			defer func() { _ = ws.Close() }()

			err := tc.list(ws)
			if err == nil {
				t.Fatalf("a %s registry listing carrying a blank %s decoded without complaint — "+
					"H-14 must fail it", tc.kind, tc.idField)
			}
			msg := err.Error()

			if !strings.Contains(msg, degeneracy.Marker) {
				t.Errorf("the wrapped error lost the %q marker the integration harness greps for: %s",
					degeneracy.Marker, msg)
			}
			if !errors.Is(err, degeneracy.ErrDegenerate) {
				t.Errorf("the wrapped error no longer answers errors.Is(ErrDegenerate): %s", msg)
			}
			// The recovery half: name the concrete way out, not just the cause.
			for _, want := range []string{tc.kind, tc.wsCmd} {
				if !strings.Contains(msg, want) {
					t.Errorf("the error does not mention %q, so the reader is still stranded: %s", want, msg)
				}
			}
		})
	}
}

// TestRegistryListHealthyPayloadCarriesNoHint is the no-false-positive half:
// the hint rides on the degenerate path only, and a healthy listing returns no
// error at all.
func TestRegistryListHealthyPayloadCarriesNoHint(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, []map[string]any{{"area_id": "kitchen", "name": "Kitchen"}})
	})
	defer srv.Close()
	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	areas, err := ws.AreaRegistryList(context.Background())
	if err != nil {
		t.Fatalf("a healthy area listing failed: %v", err)
	}
	if len(areas) != 1 || areas[0].AreaID != "kitchen" {
		t.Errorf("unexpected areas: %+v", areas)
	}
}
