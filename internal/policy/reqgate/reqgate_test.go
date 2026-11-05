package reqgate

import (
	"context"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

type nopGate struct{ name string }

func (nopGate) Check(context.Context, reqmap.Request) error                    { return nil }
func (nopGate) CheckHop(context.Context, reqmap.Request, reqmap.Request) error { return nil }
func (nopGate) Filter(_ context.Context, _ reqmap.Request, resp []byte) ([]byte, error) {
	return resp, nil
}

func (nopGate) Observe(reqmap.Request, []byte) {}

// TestInstallReplacesAndUninstallsOnlyItsOwnGate: one gate per invocation; a
// stale uninstall (an invocation that ended after the next one started) never
// removes the gate installed after it.
func TestInstallReplacesAndUninstallsOnlyItsOwnGate(t *testing.T) {
	if Current() != nil {
		t.Fatal("a gate is installed before the test")
	}
	a, b := &nopGate{"a"}, &nopGate{"b"}
	undoA := Install(a)
	if Current() != a {
		t.Fatal("Install(a) did not install a")
	}
	undoB := Install(b)
	undoA()
	if Current() != b {
		t.Errorf("a's uninstall removed b")
	}
	undoB()
	if Current() != nil {
		t.Errorf("b's uninstall left %v", Current())
	}
}

// TestCheckedMarkRoundTrips: the mark a client puts on a decided request's
// context is what the transport reads back; an unmarked context has none.
func TestCheckedMarkRoundTrips(t *testing.T) {
	if _, ok := CheckedFrom(context.Background()); ok {
		t.Fatal("an unmarked context carries a mark")
	}
	want := Checked{Request: reqmap.Request{Transport: reqmap.Companion, Method: "PUT", Path: "/v1/config/script?id=x"}, WirePath: "/api/hassio_ingress/t/v1/config/script?id=x", Prefix: "/api/hassio_ingress/t"}
	got, ok := CheckedFrom(WithChecked(context.Background(), want))
	if !ok || got.WirePath != want.WirePath || got.Prefix != want.Prefix || got.Request.Path != want.Request.Path {
		t.Errorf("mark = %+v, %v; want %+v", got, ok, want)
	}
}
