package enforce_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The answer to a write that a command only prints (D-182) — a service
// call's changed states, and its service_response — is filtered like a
// read's even in a write command, whose own reads stay exempt (D-82).

const shownPolicy = `version: 1
active: a
profiles:
  a:
    default: allow
    deny:
      "entity:lock.*": [read]
      "entity:light.a/attr/secret": [read]
`

func svcCall(query string) reqmap.Request {
	return reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/light/turn_on" + query}
}

func TestAServiceCallsAnswerIsFilteredInAWriteCommand(t *testing.T) {
	ctx := context.Background()
	s, _ := newSession(t, shownPolicy, nil, func(o *enforce.Options) { o.WriteCommand = true })
	answer := `[{"entity_id":"light.a","state":"on","attributes":{"secret":"s3","brightness":9}},{"entity_id":"lock.door","state":"unlocked","attributes":{}}]`
	out, err := s.Filter(ctx, svcCall(""), []byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "lock.door") || strings.Contains(string(out), "s3") {
		t.Errorf("a denied changed state or attribute shown: %s", out)
	}
	if !strings.Contains(string(out), `"brightness":9`) || !strings.Contains(string(out), enforce.Denied) {
		t.Errorf("the readable changed state lost or not masked: %s", out)
	}
	if h, m, _ := s.ReadCounts(); h != 1 || m != 1 {
		t.Errorf("counted hidden %d masked %d, want 1 and 1", h, m)
	}
}

func TestAServiceResponseDropsDeniedEntities(t *testing.T) {
	ctx := context.Background()
	s, _ := newSession(t, shownPolicy, nil, func(o *enforce.Options) { o.WriteCommand = true })
	answer := `{"changed_states":[{"entity_id":"lock.door","state":"locked","attributes":{}}],"service_response":{"lock.door":{"code":"1234"},"light.b":{"ok":true}}}`
	out, err := s.Filter(ctx, svcCall("?return_response=true"), []byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "lock.door") || strings.Contains(string(out), "1234") {
		t.Errorf("a denied entity in the service response shown: %s", out)
	}
	if !strings.Contains(string(out), `"light.b"`) {
		t.Errorf("a readable entity's response dropped: %s", out)
	}
	// A response the service keys by its own names passes as it is.
	plain := `{"changed_states":[],"service_response":{"forecast":[1,2],"lock.door":null}}`
	out, err = s.Filter(ctx, svcCall("?return_response=true"), []byte(plain))
	if err != nil || !strings.Contains(string(out), `"forecast":[1,2]`) || strings.Contains(string(out), "lock.door") {
		t.Errorf("service_response keys: %s %v", out, err)
	}
}

func TestAShownAnswerFilterNeverFailsTheWrite(t *testing.T) {
	ctx := context.Background()
	s, _ := newSession(t, shownPolicy, nil, func(o *enforce.Options) { o.WriteCommand = true })
	out, err := s.Filter(ctx, svcCall(""), []byte(`not json`))
	if err != nil {
		t.Fatalf("a completed write reported as failed: %v", err)
	}
	if string(out) != "[]" {
		t.Errorf("an unreadable answer shown unchecked: %s", out)
	}
	if h, _, _ := s.ReadCounts(); h != 1 {
		t.Errorf("a withheld answer counted %d hidden, want 1", h)
	}
}

func TestAnUnchangedServiceCallAnswerIsByteIdentical(t *testing.T) {
	ctx := context.Background()
	s, _ := newSession(t, shownPolicy, nil, func(o *enforce.Options) { o.WriteCommand = true })
	answer := "[ {\"entity_id\":\"light.b\",\"state\":\"on\",\"attributes\":{\"a\":\"<b>\"}} ]"
	out, err := s.Filter(ctx, svcCall(""), []byte(answer))
	if err != nil || string(out) != answer {
		t.Errorf("an answer the filter did not change was rewritten: %q %v", out, err)
	}
	if h, m, r := s.ReadCounts(); h+m+r != 0 {
		t.Errorf("counted hidden %d masked %d refused %d", h, m, r)
	}
}

func TestACachedReadIsRefusedLikeAnyRead(t *testing.T) {
	s, _ := newSession(t, `version: 1
active: a
profiles:
  a:
    default: allow
    deny:
      "system:logs": [read]
`, nil)
	r, err := policy.ParseResource("system:logs")
	if err != nil {
		t.Fatal(err)
	}
	chk := []reqmap.Check{{Verb: policy.Read, Resource: r}}
	var ref *enforce.DeniedError
	if err := s.CheckCachedRead(chk); !errors.As(err, &ref) || ref.Denied[0].Resource != "system:logs" {
		t.Fatalf("a cached read of a denied resource: %v", err)
	}
}
