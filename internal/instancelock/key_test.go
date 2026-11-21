package instancelock

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// D-152, D-168: the write lock is one per Home Assistant, keyed by its URL. Two
// spellings of one instance are one lock; two instances are two.
func TestDirForURLIsOneLockPerInstance(t *testing.T) {
	cache := t.TempDir()
	userCacheDir = func() (string, error) { return cache, nil }
	t.Cleanup(func() { userCacheDir = osUserCacheDir })

	dir := func(u string) string {
		t.Helper()
		d, err := DirForURL(u)
		if err != nil {
			t.Fatalf("DirForURL(%q): %v", u, err)
		}
		return d
	}

	base := dir("http://homeassistant.local:8123")
	if !strings.HasPrefix(base, filepath.Join(cache, "hactl", "locks")+string(filepath.Separator)) {
		t.Errorf("lock dir %s is not under the user cache dir's hactl/locks", base)
	}
	if !strings.Contains(filepath.Base(base), "homeassistant.local_8123-") {
		t.Errorf("lock dir %s does not name the instance", base)
	}
	for _, same := range []string{
		"http://homeassistant.local:8123/",
		"HTTP://HomeAssistant.Local:8123",
		"http://homeassistant.local:8123//",
		" http://homeassistant.local:8123 ",
	} {
		if got := dir(same); got != base {
			t.Errorf("%q keys %s, want the same lock as http://homeassistant.local:8123 (%s)", same, got, base)
		}
	}
	if dir("http://ha.lan") != dir("http://ha.lan:80/") {
		t.Error("http's default port is not one instance with the port left out")
	}
	if dir("https://ha.lan") != dir("https://ha.lan:443") {
		t.Error("https's default port is not one instance with the port left out")
	}
	for _, other := range []string{
		"http://homeassistant.local:8124",
		"https://homeassistant.local:8123",
		"http://other.local:8123",
		"http://homeassistant.local:8123/proxy",
	} {
		if dir(other) == base {
			t.Errorf("%q shares the lock of http://homeassistant.local:8123 — a different instance", other)
		}
	}
}

func TestDirForURLRefusesWhatItCannotKey(t *testing.T) {
	userCacheDir = func() (string, error) { return t.TempDir(), nil }
	t.Cleanup(func() { userCacheDir = osUserCacheDir })
	for _, u := range []string{"", "   ", "not a url", "http://"} {
		if d, err := DirForURL(u); err == nil {
			t.Errorf("DirForURL(%q) = %s, want an error (the caller falls back to the per-directory lock)", u, d)
		}
	}
	userCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	if d, err := DirForURL("http://ha.lan:8123"); err == nil {
		t.Errorf("DirForURL without a user cache dir = %s, want an error", d)
	}
}
