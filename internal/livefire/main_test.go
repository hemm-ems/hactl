//go:build livefire

package livefire

import (
	"os"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

var (
	rigHA    *hatest.Instance
	hactlBin string
)

func TestMain(m *testing.M) {
	// The rig profile always runs. The live profile joins it only when
	// HACTL_LIVEFIRE_DIR names a configured instance, so `go test` can never
	// wander onto somebody's house by accident.
	var code int
	opts := []hatest.Option{hatest.WithFixture("realistic")}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	binDir, err := os.MkdirTemp("", "hactl-livefire-*")
	if err != nil {
		panic(err)
	}
	if hactlBin, err = BuildHactl(binDir); err != nil {
		_ = os.RemoveAll(binDir)
		panic(err)
	}

	rigHA, code = hatest.StartMain(m, opts...)
	if code != 0 {
		_ = os.RemoveAll(binDir)
		os.Exit(code)
	}
	exit := m.Run()
	rigHA.Stop()
	// os.Exit skips deferred calls, so the build directory is removed here.
	_ = os.RemoveAll(binDir)
	os.Exit(exit)
}

// eachProfile runs a case against the rig and, when configured, the real
// instance — one body, two instances. That is the whole point of the tier: a
// claim proved against HA and kept honest by the rig cannot drift apart,
// because there is only one assertion.
func eachProfile(t *testing.T, run func(t *testing.T, tgt Target)) {
	t.Helper()

	t.Run(string(Rig), func(t *testing.T) {
		run(t, Target{Profile: Rig, Dir: rigHA.Dir(), Bin: hactlBin})
	})

	t.Run(string(Live), func(t *testing.T) {
		tgt, ok := LiveTarget(t, hactlBin)
		if !ok {
			t.Skip("set HACTL_LIVEFIRE_DIR to a configured instance to run the live profile")
		}
		run(t, tgt)
	})
}
