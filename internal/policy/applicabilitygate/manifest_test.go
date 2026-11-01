package applicabilitygate

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// The manifest half of D-110's first gate: every `<verb> <kind>:<address>`
// the `map:` comments of dev/surfaces/policy.manifest write — the contract of
// each request row and each write command's plan — names a verb the
// applicability table lists at that address. The request vectors and the
// command-plan list above are the same contract in another form; this reads
// it where it is kept, so a row added there with a new triple fails here even
// before it has a vector.

var (
	mapTriple = regexp.MustCompile(`\b(control|edit|create|delete)\s+` +
		`((?:entity|device|area|floor|label|automation|script|template|helper|dashboard|config_entry|file|service|system):` +
		"(?:\\{[^}]*\\}|[^\\s;,()+{}`'\"])*)")
	placeholder = regexp.MustCompile(`\{[^}]*\}`)
)

// mapBlocks returns the text of every `# map:` comment block of the manifest,
// its continuation lines joined with spaces, keyed by the block's first line.
func mapBlocks(t *testing.T) map[int]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(filepath.Join("..", "..", "..", "dev", "surfaces", "policy.manifest")))
	if err != nil {
		t.Fatal(err)
	}
	out := map[int]string{}
	start, block := 0, []string(nil)
	flush := func() {
		if start > 0 {
			out[start] = strings.Join(block, " ")
		}
		start, block = 0, nil
	}
	for i, line := range strings.Split(string(raw), "\n") {
		text, isComment := strings.CutPrefix(line, "#")
		text = strings.TrimSpace(text)
		switch {
		case isComment && strings.HasPrefix(text, "map:"):
			flush()
			start, block = i+1, append(make([]string, 0, 8), strings.TrimPrefix(text, "map:"))
		case isComment && start > 0 && text != "":
			block = append(block, text)
		default:
			flush()
		}
	}
	flush()
	return out
}

// concrete turns a manifest address into a sample resource of the same shape:
// each `{placeholder}` becomes a sample id, a registry field placeholder one
// closed field, a kind-wide `*` one id (parse).
func concrete(t *testing.T, addr string) policy.Resource {
	t.Helper()
	addr = strings.TrimRight(addr, ".:")
	addr = strings.ReplaceAll(addr, "registry/{field}", "registry/labels")
	kind, _, _ := strings.Cut(addr, ":")
	switch kind {
	case "helper":
		addr = strings.Replace(addr, "{?domain}", "input_boolean", 1)
		addr = strings.Replace(addr, "{domain}", "input_boolean", 1)
	case "file":
		addr = placeholder.ReplaceAllString(addr, "x.yaml")
	}
	return parse(t, placeholder.ReplaceAllString(addr, "x1"))
}

func TestEveryManifestMappingIsInTheApplicabilityTable(t *testing.T) {
	n := 0
	for line, text := range mapBlocks(t) {
		for _, m := range mapTriple.FindAllStringSubmatch(text, -1) {
			n++
			r := concrete(t, m[2])
			if got := verbNames(policy.ApplicableVerbs(r)); !slices.Contains(got, m[1]) {
				t.Errorf("policy.manifest line %d maps %s %s, but the table lists %v at %s", line, m[1], m[2], got, shape(r))
			}
		}
	}
	// The manifest writes dozens of mappings; finding few means the reader
	// broke, not that the table is complete.
	if n < 40 {
		t.Fatalf("read only %d mappings from policy.manifest — the gate would compare almost nothing", n)
	}
}
