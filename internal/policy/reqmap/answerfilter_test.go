package reqmap_test

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// TestAnswerFilterNamesOnlyShownWriteAnswers — D-182: the answer to a
// service call is filtered for display; a read's answer, a write whose
// answer is not marked, and an unknown request get no answer filter here.
func TestAnswerFilterNamesOnlyShownWriteAnswers(t *testing.T) {
	cases := []struct {
		req  reqmap.Request
		want reqmap.FilterKind
	}{
		{reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/light/turn_on"}, reqmap.FilterServiceCall},
		{reqmap.Request{Transport: reqmap.REST, Method: "post", Path: "/api/services/script/x?return_response=true"}, reqmap.FilterServiceCall},
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/services"}, reqmap.FilterNone},
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states/light.a"}, reqmap.FilterNone},
		{reqmap.Request{Transport: reqmap.REST, Method: "DELETE", Path: "/api/config/config_entries/entry/x"}, reqmap.FilterNone},
		{reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/../states"}, reqmap.FilterNone},
		{reqmap.Request{Transport: reqmap.WS, Type: "call_service"}, reqmap.FilterNone},
	}
	for _, c := range cases {
		if got := reqmap.AnswerFilter(c.req).Kind; got != c.want {
			t.Errorf("%s %s %s%s: %q, want %q", c.req.Transport, c.req.Method, c.req.Path, c.req.Type, got, c.want)
		}
	}
}
