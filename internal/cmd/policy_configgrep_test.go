package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
)

// TestPolicyConfigGrepCountsOnlyUnreadFiles (D-212): the relabel check's
// config grep counts a file the walk skipped as unknown hits only when the
// file is there and went unread; a missing include target (Home Assistant
// reads it as empty, D-31) names nothing. Each hit keeps its path, so it can
// be placed in its automation or script.
func TestPolicyConfigGrepCountsOnlyUnreadFiles(t *testing.T) {
	for _, c := range []struct {
		reason    string
		truncated bool
	}{
		{companion.SkipMissing, false},
		{companion.SkipUnreadable, true},
		{companion.SkipUnparseable, true},
		{"", false},
	} {
		t.Run(c.reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/ref/grep" {
					http.NotFound(w, r)
					return
				}
				ans := map[string]any{
					"term": r.URL.Query().Get("term"), "regex": true, "case_sensitive": false, "total": 1, "truncated": false,
					"hits":    []map[string]any{{"location": "automations.yaml", "path": "[5].actions[0].target.label_id", "key": "label_id", "kind": "value", "matched_value": "downstairs"}},
					"sources": []map[string]any{{"location": "automations.yaml", "key_hits": 0, "value_hits": 1}},
				}
				if c.reason != "" {
					ans["skipped"] = []map[string]any{{"location": "themes", "reason": c.reason}}
				}
				_ = json.NewEncoder(w).Encode(ans)
			}))
			defer srv.Close()
			grep := policyConfigGrep(func(context.Context) (*companion.Client, error) { return companion.New(srv.URL, "t"), nil })
			res, err := grep(context.Background(), "downstairs")
			if err != nil {
				t.Fatal(err)
			}
			if res.Truncated != c.truncated {
				t.Errorf("truncated %v, want %v", res.Truncated, c.truncated)
			}
			if len(res.Hits) != 1 || res.Hits[0].Path != "[5].actions[0].target.label_id" || res.Hits[0].Key != "label_id" {
				t.Errorf("hits %+v", res.Hits)
			}
		})
	}
}
