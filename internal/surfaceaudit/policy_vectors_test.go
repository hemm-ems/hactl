package surfaceaudit_test

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// policyRequestRow is one ws/rest/companion row of dev/surfaces/policy.manifest
// with the class its `map:` comment gives it.
type policyRequestRow struct {
	key   string
	class string // "write", "read-deferred" or "exempt"
	line  int
}

// policyRequestRows reads the request rows of the policy manifest and the class
// of each from the `# map:` comment block above it (dev/surfaces/policy.manifest
// header: the comment is the contract, the line its proof). cmd rows are the
// command layer's and are left out: they are proven by preview refusals, not by
// a request vector.
func policyRequestRows(t *testing.T, root string) []policyRequestRow {
	t.Helper()
	path := filepath.Join(root, surfaceaudit.ManifestDir, "policy.manifest")
	f, err := os.Open(path) //nolint:gosec // fixed path under the repository root
	if err != nil {
		t.Fatalf("opening the policy manifest: %v", err)
	}
	defer func() { _ = f.Close() }()

	var rows []policyRequestRow
	class := ""
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			class = ""
		case strings.HasPrefix(line, "#"):
			body := strings.TrimSpace(strings.TrimPrefix(line, "#"))
			if after, ok := strings.CutPrefix(body, "map:"); ok {
				after = strings.TrimSpace(after)
				switch {
				case strings.HasPrefix(after, "exempt"):
					class = "exempt"
				case strings.HasPrefix(after, "read-deferred-phase3"):
					class = "read-deferred"
				default:
					class = "write"
				}
			}
		default:
			key, _, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			if strings.HasPrefix(key, "ws ") || strings.HasPrefix(key, "rest ") || strings.HasPrefix(key, "companion ") {
				if class == "" {
					t.Errorf("%s:%d: request row %q has no `# map:` comment above it — the comment is the contract", path, n, key)
				}
				rows = append(rows, policyRequestRow{key: key, class: class, line: n})
			}
			class = ""
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading the policy manifest: %v", err)
	}
	return rows
}

// requestVectorFile is the slice of testdata/policy/requests/vectors.json this
// package reads: which row each vector claims and what it expects. The vectors
// themselves are run against the classifier in internal/policy/reqmap.
type requestVectorFile struct {
	Vectors []struct {
		ID      string `json:"id"`
		Request struct {
			Transport string `json:"transport"`
		} `json:"request"`
		Expect struct {
			Disposition string `json:"disposition"`
			Row         string `json:"row"`
			Refused     bool   `json:"refused"`
		} `json:"expect"`
	} `json:"vectors"`
	Redirects []struct {
		ID string `json:"id"`
	} `json:"redirects"`
}

// deadRoutes are the request keys of the client methods D-81 deletes. None may
// come back as a manifest row; testdata/policy/requests/vectors.json holds a
// vector for each asserting it is unmapped (comp-dead-*, rest-dead-*).
// `companion GET /v1/config/templates` came back with `policy lint`'s
// template enumeration (D-116) and is a mapped read row again.
var deadRoutes = []string{
	"companion PUT /v1/config/file",
	"companion PUT /v1/config/template",
	"companion GET /v1/config/scripts",
	"companion GET /v1/config/automations",
	"companion PUT /v1/config/helper",
	"companion POST /v1/ha/reload/{domain}",
	"rest GET /api/config/automation/config/{automation_id}",
	"rest GET /api/events",
}

// TestEveryPolicyRequestRowHasAVector — the request-mapping table is closed
// over the manifest it proves (H-29, "one case per mapped row").
//
// It needs no classifier: it holds the vector table to the manifest, both ways.
// Every ws/rest/companion row has at least one vector naming it whose expected
// disposition is the row's own `map:` class; every vector that names a row
// names one that exists, with the transport its key starts with. So a row added
// to the manifest without a vector, a vector left behind by a deleted row, and
// a vector that quietly expects a write row to be a read all fail here, before
// the classifier is even consulted.
// ownerOnlyRows are the write rows whose mapping always refuses while a
// profile is in effect: only an owner's command, which runs without a policy
// session, sends them (D-149, D-160). A refused vector is their mapping.
var ownerOnlyRows = map[string]bool{"ws auth/long_lived_access_token": true}

func TestEveryPolicyRequestRowHasAVector(t *testing.T) {
	root := repoRoot(t)
	rows := policyRequestRows(t, root)
	if len(rows) == 0 {
		t.Fatal("no request row found in dev/surfaces/policy.manifest — the reader has stopped matching")
	}

	raw, err := os.ReadFile(filepath.Join(root, "testdata", "policy", "requests", "vectors.json")) //nolint:gosec // fixed path under the repository root
	if err != nil {
		t.Fatalf("reading the request vectors: %v", err)
	}
	var vf requestVectorFile
	if err := json.Unmarshal(raw, &vf); err != nil {
		t.Fatalf("decoding the request vectors: %v", err)
	}
	if len(vf.Vectors) == 0 {
		t.Fatal("the request vector table is empty")
	}

	byKey := map[string]policyRequestRow{}
	for _, r := range rows {
		byKey[r.key] = r
	}

	ids := map[string]bool{}
	covered := map[string]bool{}
	for _, v := range vf.Vectors {
		if ids[v.ID] {
			t.Errorf("vector id %q is used twice", v.ID)
		}
		ids[v.ID] = true
		if v.Expect.Row == "" {
			continue
		}
		r, ok := byKey[v.Expect.Row]
		if !ok {
			t.Errorf("vector %s names row %q, which dev/surfaces/policy.manifest does not have", v.ID, v.Expect.Row)
			continue
		}
		if !strings.HasPrefix(v.Expect.Row, v.Request.Transport+" ") {
			t.Errorf("vector %s sends a %s request but names row %q", v.ID, v.Request.Transport, v.Expect.Row)
		}
		if v.Expect.Disposition == r.class || (ownerOnlyRows[r.key] && v.Expect.Disposition == "unmappable" && v.Expect.Refused) {
			covered[r.key] = true
		}
	}
	for _, r := range vf.Redirects {
		if ids[r.ID] {
			t.Errorf("redirect vector id %q is used twice", r.ID)
		}
		ids[r.ID] = true
	}

	for _, r := range rows {
		if !covered[r.key] {
			t.Errorf("policy.manifest:%d: row %q (map: %s) has no vector expecting %q — add one to testdata/policy/requests/vectors.json",
				r.line, r.key, r.class, r.class)
		}
	}

	for _, dead := range deadRoutes {
		if _, ok := byKey[dead]; ok {
			t.Errorf("%q is a manifest row again, but D-81 deleted the client method that sent it", dead)
		}
	}
}
