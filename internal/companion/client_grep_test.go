package companion

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestRefGrep pins the read-only grep route's request and decode: the term is
// sent verbatim, the two mode flags only when set, and every documented field
// reaches the struct — including `truncated`, which is decoded rather than
// inferred so a capped answer cannot read as a complete one.
func TestRefGrep(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/grep" || r.Method != http.MethodGet {
			t.Errorf("request = %s %s, want GET /v1/ref/grep", r.Method, r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"term":"Küche","regex":true,"case_sensitive":true,
			"hits":[{"location":"scripts.yaml","path":"kueche_licht.alias","key":"alias","kind":"value","matched_value":"Küche Licht"}],
			"total":1500,"truncated":true,
			"sources":[{"location":"scripts.yaml","key_hits":3,"value_hits":1497}],
			"skipped":[{"location":"packages/x.yaml","reason":"unreadable"}]}`)
	}))
	defer srv.Close()

	r, err := New(srv.URL, "t").RefGrep(context.Background(), "Küche", true, true)
	if err != nil {
		t.Fatalf("RefGrep: %v", err)
	}
	if want := "case_sensitive=true&regex=true&term=K%C3%BCche"; gotQuery != want {
		t.Errorf("query = %q, want %q", gotQuery, want)
	}
	if !r.Truncated || r.Total != 1500 || len(r.Hits) != 1 || r.Hits[0].Kind != GrepKindValue ||
		r.Hits[0].MatchedValue != "Küche Licht" || r.Sources[0].KeyHits != 3 || len(r.Skipped) != 1 {
		t.Errorf("decoded %+v", r)
	}

	if _, err := New(srv.URL, "t").RefGrep(context.Background(), "licht", false, false); err != nil {
		t.Fatal(err)
	}
	if want := "term=licht"; gotQuery != want {
		t.Errorf("default mode query = %q, want %q (no mode flags sent)", gotQuery, want)
	}
}

// TestRefGrepRefusalIsATypedStatus: the companion refuses a term or pattern
// with 400, and `grep` ends the command on exactly that status — so the client
// must surface it typed, and must not retry it (it would be refused again).
func TestRefGrepRefusalIsATypedStatus(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, `{"error":"Invalid regex: missing )"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t").RefGrep(context.Background(), "a(", true, false)
	if status, ok := haapi.HTTPStatus(err); !ok || status != http.StatusBadRequest {
		t.Fatalf("want a typed 400, got %v", err)
	}
	var se *haapi.HTTPStatusError
	if !errors.As(err, &se) || se.Path != "/v1/ref/grep" {
		t.Errorf("the error must name the route: %v", err)
	}
	if calls != 1 {
		t.Errorf("a 400 was sent %d times; it must not be retried", calls)
	}
}
