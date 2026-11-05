package instancelock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DirForURL is the directory holding the write lock of the Home Assistant at
// haURL (D-152, D-168): one lock per Home Assistant, not per instance
// directory, so two agents' directories on one Home Assistant (D-151) take
// the same lock and their confirmed writes serialize.
//
// It lives under the user's cache directory, `<cache>/hactl/locks/<host>_<port>-<hash>`.
// The host and port make the "lock held" error name the instance; the hash
// (the first 12 hex digits of the SHA-256 of the normalized URL) makes two
// instances that share a host and port but not a path two locks.
//
// Spellings that differ only in the case of scheme and host, a default port
// (80 for http, 443 for https) or trailing slashes are one instance. Anything
// else — an IP for a host name, a proxy — is another key: a lock on a local
// file cannot know two URLs reach one server (INVARIANTS.md H-26).
//
// An empty or unparsable URL, or no user cache directory, is an error: the
// caller falls back to the per-directory lock.
func DirForURL(haURL string) (string, error) {
	key, label, err := urlKey(haURL)
	if err != nil {
		return "", err
	}
	cache, err := userCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(cache, "hactl", "locks", label+"-"+hex.EncodeToString(sum[:])[:12]), nil
}

// userCacheDir is os.UserCacheDir, a variable so a test can take it away.
var userCacheDir = osUserCacheDir

// osUserCacheDir is the real one; tests restore it.
var osUserCacheDir = os.UserCacheDir

// urlKey normalizes haURL to the string the lock is keyed by and a readable
// `<host>_<port>` label for the directory name.
func urlKey(haURL string) (key, label string, err error) {
	raw := strings.TrimSpace(haURL)
	if raw == "" {
		return "", "", errors.New("no Home Assistant URL to key the write lock by")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if u.Host == "" {
		return "", "", errors.New("the Home Assistant URL " + raw + " names no host")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case port == "" && scheme == "https":
		port = "443"
	case port == "":
		port = "80"
	}
	hostPart := host
	if strings.Contains(host, ":") { // an IPv6 literal keeps its brackets
		hostPart = "[" + host + "]"
	}
	u.Host = hostPart
	if (scheme != "http" || port != "80") && (scheme != "https" || port != "443") {
		u.Host = hostPart + ":" + port
	}
	u.Scheme = scheme
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), sanitize(host) + "_" + port, nil
}

// sanitize keeps a host readable in a file name: letters, digits, `.` and
// `-`; anything else becomes `_`.
func sanitize(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
