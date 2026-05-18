//go:build integration

package integration

import (
	"os"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ha is the shared HA instance for all integration tests in this package.
// Started once in TestMain, reused across all tests.
var ha *hatest.Instance

func TestMain(m *testing.M) {
	var code int
	opts := []hatest.Option{hatest.WithFixture("basic")}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	ha, code = hatest.StartMain(m, opts...)
	if code != 0 {
		os.Exit(code)
	}
	exitCode := m.Run()
	ha.Stop()
	if faultyHA != nil {
		faultyHA.Stop()
	}
	if realisticHA != nil {
		realisticHA.Stop()
	}
	os.Exit(exitCode)
}
