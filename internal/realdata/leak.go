package realdata

import (
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// The leak gate
// ---------------------------------------------------------------------------
//
// Two independent checks, per SPEC-realdata-fixture.md A3, and they are
// independent on purpose: each catches what the other cannot.
//
//   - [SensitiveLiterals] + [Contains] is the DERIVED check. It extracts every
//     value in a known-sensitive position from the real source and asserts that
//     no member appears in the derivative. It is exact, and it is blind to any
//     leak the source it was given does not contain.
//   - [ShapeLeaks] is the check that does not need the source at all. It reads
//     the derivative alone and refuses an IPv4 outside the documentation
//     ranges, a MAC outside a synthetic locally-administered range, and a
//     coordinate that is not one of the documentation values. That is what
//     catches a leak introduced by a FUTURE capture the first check never saw.
//
// The second is the one that matters in a year. The first can only ever be as
// complete as the snapshot somebody remembered to point it at.

// Leak is one finding: a value that must not be in the published tree, and
// where it was found.
type Leak struct {
	File   string
	Line   int
	Value  string
	Reason string
}

func (l Leak) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", l.File, l.Line, l.Value, l.Reason)
}

// sensitiveKeys are the YAML/JSON keys whose VALUE identifies a person, a
// place, or an account.
//
// This is a list of POSITIONS, not of values, and the difference is the whole
// design. A list of values is a denylist: it protects against the leaks
// somebody already thought of. A list of positions extracts whatever happens to
// be sitting there, including the value nobody predicted — which is how the
// 24 plaintext WiFi passwords in esphome/*.yaml (§11) come out without anyone
// having to know they existed.
var sensitiveKeys = map[string]string{
	"latitude":      "coordinate",
	"longitude":     "coordinate",
	"name":          "human-authored name",
	"name_by_user":  "human-authored name",
	"alias":         "human-authored name",
	"friendly_name": "human-authored name",
	"title":         "human-authored name",
	"password":      "credential",
	"api_key":       "credential",
	"api_password":  "credential",
	"token":         "credential",
	"access_token":  "credential",
	"secret":        "credential",
	"key":           "credential",
	"encryption":    "credential",
	"psk":           "credential",
	"ssid":          "network identifier",
	"host":          "network identifier",
	"hostname":      "network identifier",
	"url":           "network identifier",
	"mac":           "network identifier",
	"ip":            "network identifier",
	"ip_address":    "network identifier",
	"email":         "account identifier",
	"username":      "account identifier",
	"user":          "account identifier",
}

var (
	// keyValue matches `key: value` in YAML and `"key": "value"` in JSON with
	// one expression, because the tree holds both and a leak does not care
	// which file it is in.
	//
	// The horizontal-whitespace classes are load-bearing and were not there
	// first. `\s` matches a newline, so with `(?m)` the leading `\s*` ran off
	// the end of one line and the value group swallowed the next: on the
	// fixture's own configuration.yaml, `homeassistant:` matched with the value
	// "latitude: 48.137154", and `latitude` was consumed rather than extracted.
	// A gate that under-matches is silent about exactly the thing it exists to
	// find — TestLeakGateFlagsATreeThatLeaks is what turned that up.
	keyValue = regexp.MustCompile(`(?m)^[ \t-]*"?([A-Za-z_][A-Za-z0-9_]*)"?[ \t]*:[ \t]*(.+?)[ \t]*,?[ \t]*$`)
	ipv4     = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	macAddr  = regexp.MustCompile(`(?i)\b([0-9a-f]{2}(?::[0-9a-f]{2}){5})\b`)
	// A bare decimal with five or more fractional digits is the shape a
	// coordinate takes when it is not under a `latitude:` key — in a template,
	// in a URL, in a comment. Catching it by shape is what makes the second
	// gate independent of any capture.
	//
	// Five, not three, and timestamps stripped first: at three the gate fired on
	// an input_number's `"step": 0.001` and on the `00.000000` inside every
	// `2026-01-01T00:00:00.000000+00:00` in the registries. Neither is a
	// location and a gate that cries wolf on a step value is a gate somebody
	// switches off. Real coordinates carry six.
	preciseDecimal = regexp.MustCompile(`(-?\d{1,3}\.\d{5,})`)
	// isoTimestamp is removed before the coordinate scan, because its
	// fractional seconds are decimal digits like any other.
	isoTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?([+-]\d{2}:\d{2}|Z)?`)
)

// SensitiveLiterals extracts every value sitting in a sensitive position
// anywhere under root.
//
// Values shorter than three runes are dropped: "on", "1" and "" occur in every
// document ever written, and a gate that flagged them would report a leak on
// any tree at all and be switched off within a week.
func SensitiveLiterals(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G304: a path under the caller's own capture directory
		if readErr != nil {
			// A file this process cannot read carries no literals to compare
			// against. The DERIVATIVE is a different matter and does fail on a
			// read error — a fixture file the gate cannot open is a fixture
			// file the gate has not checked.
			return nil //nolint:nilerr // deliberate: an unreadable source file is skipped, not fatal
		}
		for _, m := range keyValue.FindAllStringSubmatch(string(data), -1) {
			reason, sensitive := sensitiveKeys[strings.ToLower(m[1])]
			if !sensitive {
				continue
			}
			for _, value := range literalCandidates(m[2]) {
				out[value] = reason
			}
		}
		// Only ADDRESSES THAT IDENTIFY somebody. 127.0.0.1 and 192.168.1.10
		// appear in the reference config and in the fixture, and they name no
		// house in particular — the same judgement ShapeLeaks makes, made once
		// and shared, so the two gates cannot come to different conclusions
		// about the same address.
		for _, m := range ipv4.FindAllStringSubmatch(string(data), -1) {
			if !documentationIPv4(m[1]) {
				out[m[1]] = "IPv4 address"
			}
		}
		for _, m := range macAddr.FindAllStringSubmatch(string(data), -1) {
			if !syntheticMAC(m[1]) {
				out[m[1]] = "MAC address"
			}
		}
		return nil
	})
	return out, err
}

// literalCandidates normalises a matched value into the forms it could appear
// as in the derivative: unquoted, and with any YAML tag or comment removed.
func literalCandidates(raw string) []string {
	v := strings.TrimSpace(raw)
	v = strings.Trim(v, `"'`)
	if v == "" || runeLen(v) < 3 {
		return nil
	}
	// A tagged value (`!secret shape_api_token`) names a key, not a secret; the
	// secret itself is extracted where secrets.yaml defines it.
	if strings.HasPrefix(v, "!") {
		return nil
	}
	// Structural values carry no information about anybody.
	switch strings.ToLower(v) {
	case "true", "false", "null", "none", "{}", "[]", "|", ">", ">-", "|-":
		return nil
	}
	return []string{v}
}

// distinctiveLiteral reports whether a literal is specific enough to be
// searched for ANYWHERE in the derivative, rather than only as a whole value.
//
// This is the rule that decides whether the derived gate is usable, and length
// turned out to be the wrong question. Extracting every `name:` value from a
// real config yields identifying material and ordinary vocabulary in the same
// heap: the reference instance's 49,158 lines contribute "Button",
// "Temperature", "Volume", "Light" and "Map" alongside the names of its
// occupants. Searching for those as substrings reports a leak on any English
// text at all — including on the generator's own synthetic vocabulary, which is
// drawn from the same household words on purpose. A gate that fires there is a
// gate somebody switches off, and the real leak leaves with it.
//
// What actually separates the two heaps is not size but SHAPE. Identifying
// material is a phrase ("Anwesenheit Küche"), or carries non-ASCII, or contains
// something that is not a letter — a digit, a dot, a hyphen, an @ — because
// hostnames, credentials, coordinates and slugs all do. A single run of letters
// is indistinguishable from vocabulary, so it is only alarming when it IS the
// whole value.
//
// The second clause is the same argument made about the other heap. The
// reference instance has three input_numbers named "1. ", "2. " and "3. ", and
// each is a `name:` value with a non-letter in it, so the shape rule alone
// searches the whole tree for the three-character string "2. " — which occurs
// inside any numbered list anybody ever wrote, and does occur inside a
// sanitized automation description. A value with no letters carries no words:
// as a whole value it is still compared, and if it is a number long enough to
// identify an account or a phone it is still searched for, but three characters
// of punctuation are noise.
//
// Nothing identifying escapes through that: coordinates, MACs and routable
// IPv4s are caught by ShapeLeaks whatever they look like, and that gate does
// not consult this list at all.
func distinctiveLiteral(value string) bool {
	distinctive, letters := false, false
	for _, r := range value {
		if unicode.IsLetter(r) {
			letters = true
		}
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && r != '\'') {
			distinctive = true
		}
	}
	return distinctive && (letters || runeLen(value) >= 6)
}

// Contains reports every sensitive literal that survived into the derivative.
func Contains(derivative string, literals map[string]string) ([]Leak, error) {
	var leaks []Leak
	err := filepath.WalkDir(derivative, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
		if readErr != nil {
			return readErr
		}
		rel := relOrPath(derivative, path)
		for i, line := range strings.Split(string(data), "\n") {
			if structuralPosition(line) {
				continue
			}
			whole := wholeValue(line)
			for value, reason := range literals {
				hit := whole == value
				if distinctiveLiteral(value) {
					hit = strings.Contains(line, value)
				}
				if hit {
					leaks = append(leaks, Leak{File: rel, Line: i + 1, Value: value, Reason: reason})
				}
			}
		}
		return nil
	})
	return leaks, err
}

// structuralPositions are keys whose values come from a fixed public
// vocabulary, so they identify nobody and are carried into the fixture
// verbatim and on purpose.
//
// This is an exemption by POSITION, like sensitiveKeys is a selection by
// position — not a list of allowed values, which would be the denylist
// inversion this package refuses. A unit of measurement is `kWh`, `°C`, `W`,
// `€/kWh`; an icon is a Material Design Icons name; `mode`, `device_class` and
// `state_class` are Home Assistant enums. Each is also, coincidentally, a
// `name:` value somewhere in the reference instance's 49,158 lines, which is
// how they ended up in the extracted set at all.
//
// Carrying them is deliberate. The instance's icon set contains a real typo
// (`mdi:pressence`) — exactly the sort of value a hand-authored fixture would
// never contain, and one that identifies nobody.
var structuralPositions = map[string]bool{
	"unit_of_measurement": true,
	"icon":                true,
	"mode":                true,
	"device_class":        true,
	"state_class":         true,
}

// structuralPosition reports whether a line assigns one of those keys.
func structuralPosition(line string) bool {
	m := keyValue.FindStringSubmatch(line)
	return m != nil && structuralPositions[strings.ToLower(m[1])]
}

// wholeValue returns the value a `key: value` line assigns, unquoted, or "".
func wholeValue(line string) string {
	m := keyValue.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.Trim(strings.TrimSpace(m[2]), `"',`)
}

// ShapeLeaks reads the derivative alone and refuses anything that has the shape
// of real-world data, whether or not it appears in any capture.
//
// This is the half that survives the next capture. The derived check above can
// only compare against the snapshot it was handed; a value introduced by a
// later regeneration, from a part of the instance nobody archived, is invisible
// to it and caught here.
func ShapeLeaks(derivative string) ([]Leak, error) {
	var leaks []Leak
	err := filepath.WalkDir(derivative, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
		if readErr != nil {
			return readErr
		}
		rel := relOrPath(derivative, path)
		for i, line := range strings.Split(string(data), "\n") {
			leaks = append(leaks, shapeLeaksInLine(rel, i+1, line)...)
		}
		return nil
	})
	return sortLeaks(leaks), err
}

// sortLeaks makes the report a function of the tree, not of map iteration
// order (H-16). Contains walks the extracted literals, and Go randomises that
// walk per run — so without this the same fixture produced the same FINDINGS in
// a different order every time, and a reviewer diffing two runs could not tell
// a new leak from a reshuffle.
func sortLeaks(leaks []Leak) []Leak {
	sort.Slice(leaks, func(i, j int) bool {
		if leaks[i].File != leaks[j].File {
			return leaks[i].File < leaks[j].File
		}
		if leaks[i].Line != leaks[j].Line {
			return leaks[i].Line < leaks[j].Line
		}
		return leaks[i].Value < leaks[j].Value
	})
	return leaks
}

func shapeLeaksInLine(file string, line int, text string) []Leak {
	var leaks []Leak
	for _, m := range ipv4.FindAllStringSubmatch(text, -1) {
		if !documentationIPv4(m[1]) {
			leaks = append(leaks, Leak{File: file, Line: line, Value: m[1],
				Reason: "IPv4 outside RFC 5737 documentation and RFC 1918 private ranges"})
		}
	}
	for _, m := range macAddr.FindAllStringSubmatch(text, -1) {
		if !syntheticMAC(m[1]) {
			leaks = append(leaks, Leak{File: file, Line: line, Value: m[1],
				Reason: "MAC outside the synthetic locally-administered range 02:00:5e:*"})
		}
	}
	// A line carrying a Jinja expression is doing arithmetic, not stating a
	// location. The reference instance's template.yaml computes saturation
	// vapour pressure — `2.718281828459045`, `0.000031865`, `0.01416` — and a
	// gate that calls Euler's number a coordinate is a gate that gets switched
	// off before it ever meets a real one. A coordinate reaches a config as a
	// VALUE, and a value under a coordinate key is caught by the key-position
	// rule in SensitiveLiterals rather than here.
	if strings.Contains(text, "{{") || strings.Contains(text, "{%") {
		return leaks
	}
	for _, m := range preciseDecimal.FindAllStringSubmatch(isoTimestamp.ReplaceAllString(text, "<ts>"), -1) {
		if !documentationCoordinate(m[1]) {
			leaks = append(leaks, Leak{File: file, Line: line, Value: m[1],
				Reason: "a decimal precise enough to be a coordinate, and not one of the documentation values"})
		}
	}
	return leaks
}

// documentationIPv4 accepts the three RFC 5737 documentation blocks, RFC 1918
// private space, and loopback.
//
// Private space is allowed because a fixture legitimately points an integration
// at a local address, and because a 192.168.x.y in a published fixture
// identifies nobody — every home network on earth uses the same block.
func documentationIPv4(s string) bool {
	addr, err := netip.ParseAddr(s)
	if err != nil || !addr.Is4() {
		return false
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() || addr.IsMulticast() {
		return true
	}
	for _, block := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		if netip.MustParsePrefix(block).Contains(addr) {
			return true
		}
	}
	return false
}

// syntheticMAC accepts only the locally-administered prefix the generator
// mints, so a real NIC's address cannot pass as "probably fine".
func syntheticMAC(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "02:00:5e:")
}

// documentationCoordinate accepts the coordinates the fixture is allowed to
// carry: Home Assistant's own demo location, and zero.
func documentationCoordinate(s string) bool {
	switch s {
	case "52.520", "13.405", "52.5200", "13.4050", "0.000", "-0.000":
		return true
	}
	// A version-like or plainly-not-a-coordinate value: out of latitude and
	// longitude range entirely.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return true
	}
	return f > 180 || f < -180
}

func relOrPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
