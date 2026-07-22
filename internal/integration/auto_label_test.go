//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestAutoLsLabelFromRegistry proves `auto ls --label` against a real Home
// Assistant: it creates a label, attaches it to an automation's entity registry
// entry, and requires both the labels column and the --label filter to reflect it.
//
// This is an integration test rather than a unit test on purpose. `auto ls` read
// labels from /api/states entity attributes, where HA has never put them — an
// automation's attributes are exactly [current friendly_name id last_triggered
// mode]. So the labels column was always blank and `--label` always matched
// nothing, while docs/manual.md promised the feature worked. Any test that
// supplies its own idea of what HA returns can reproduce that same mistake; only
// a real instance settles where labels actually live.
func TestAutoLsLabelFromRegistry(t *testing.T) {
	autoID := getFirstAutoID(t)
	entityID := "automation." + autoID

	// A label name that appears nowhere else in the output — an id substring
	// would make the assertion pass for the wrong reason.
	const labelName = "roundtrip-label-zx9"

	cfg := loadConfig(t)
	ctx := context.Background()
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("connecting to HA: %v", err)
	}
	// Register the close as a cleanup rather than deferring it: t.Cleanup runs
	// after the function returns, so a deferred Close would shut the socket
	// before the detach below could use it — and the label would leak into the
	// container this package shares, widening the labels column for every later
	// test. Cleanups run LIFO, so this one registers first to run last.
	t.Cleanup(func() { _ = ws.Close() })

	label, err := ws.LabelRegistryCreate(ctx, labelName, "blue", "", "")
	if err != nil {
		t.Fatalf("creating label: %v", err)
	}
	t.Cleanup(func() {
		if err := ws.LabelRegistryDelete(context.Background(), label.LabelID); err != nil {
			t.Errorf("cleanup: deleting label %s: %v", label.LabelID, err)
		}
	})

	if err := ws.EntityRegistryUpdate(ctx, entityID, map[string]any{
		"labels": []string{label.LabelID},
	}); err != nil {
		t.Fatalf("attaching label to %s: %v", entityID, err)
	}
	// Detaching must be asserted, not best-effort: a silent failure here leaks
	// the label into every subsequent test in this package.
	t.Cleanup(func() {
		if err := ws.EntityRegistryUpdate(context.Background(), entityID, map[string]any{
			"labels": []string{},
		}); err != nil {
			t.Errorf("cleanup: detaching label from %s: %v", entityID, err)
		}
	})

	out := runHactl(t, "auto", "ls")
	if !strings.Contains(out, labelName) {
		t.Errorf("labels column does not show the registry label %q:\n%s", labelName, out)
	}

	filtered := runHactl(t, "auto", "ls", "--label", labelName)
	if !strings.Contains(filtered, autoID) {
		t.Errorf("auto ls --label %s did not return %s:\n%s", labelName, autoID, filtered)
	}

	// The filter must exclude as well as include, or "matches everything" would
	// pass the assertion above.
	none := runHactl(t, "auto", "ls", "--label", "no-such-label-qq7")
	if strings.Contains(none, autoID) {
		t.Errorf("auto ls --label with an unknown label still returned %s:\n%s", autoID, none)
	}
}
