package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// Read enforcement at the CLI (W-2, manual "Enforcement (reads)"): what a
// command prints is what the request layer's filter left it (D-138), and the
// `hidden by policy` trailer is rendered once per invocation — the last
// stdout line in prose, stderr under --json (D-139).

const readsPolicy = `version: 1
active: agent
profiles:
  agent:
    deny:
      "entity:binary_sensor.*": [read]
      "entity:light.kitchen/state": [read]
`

// runReads runs one command in process with its stderr captured, as the MCP
// server does (mcpserver.WithStderr).
func runReads(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	full := append(append([]string{"hactl"}, args...), "--dir", dir, "--timeout", "3s", "--tokensmax", "0")
	err = RunWithOutputContext(mcpserver.WithStderr(context.Background(), &errOut), full, &out)
	return out.String(), errOut.String(), err
}

// TestReadTrailerInProseAndJSON — a listing drops the rows the profile may not
// read and masks the parts, and says so once: prose ends with the trailer
// line; --json keeps its shape (an array that parses) and the same line goes
// to stderr.
func TestReadTrailerInProseAndJSON(t *testing.T) {
	f := buildContractFixture(t)
	writePolicyFile(t, f.dir, readsPolicy)
	const trailer = "hidden by policy: 1, masked: 1 (profile agent)"

	out, errOut, err := runReads(t, f.dir, "ent", "ls")
	if err != nil {
		t.Fatalf("ent ls: %v\n%s", err, out)
	}
	if strings.Contains(out, "binary_sensor.door") {
		t.Errorf("ent ls shows an entity the profile may not read:\n%s", out)
	}
	if !strings.Contains(out, "<denied>") {
		t.Errorf("ent ls does not show the masked state as <denied>:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if last := lines[len(lines)-1]; last != trailer {
		t.Errorf("prose: last line %q, want %q", last, trailer)
	}
	if errOut != "" {
		t.Errorf("prose wrote the trailer to stderr too: %q", errOut)
	}

	out, errOut, err = runReads(t, f.dir, "ent", "ls", "--json")
	if err != nil {
		t.Fatalf("ent ls --json: %v\n%s", err, out)
	}
	var rows []map[string]any
	if jerr := json.Unmarshal([]byte(out), &rows); jerr != nil {
		t.Fatalf("--json does not keep its shape: %v\n%s", jerr, out)
	}
	for _, r := range rows {
		if r["entity_id"] == "binary_sensor.door" {
			t.Errorf("--json shows an entity the profile may not read: %v", r)
		}
	}
	if strings.Contains(out, "hidden by policy") {
		t.Errorf("--json carries the trailer on stdout:\n%s", out)
	}
	if errOut != trailer+"\n" {
		t.Errorf("--json stderr %q, want the trailer line %q", errOut, trailer)
	}
}

// TestReadingOneDeniedThingIsRefused — `ent show` of a denied entity is
// refused like a denied write: exit 1, policy_denied, the rule and its line;
// nothing of it on stdout, no trailer.
func TestReadingOneDeniedThingIsRefused(t *testing.T) {
	f := buildContractFixture(t)
	writePolicyFile(t, f.dir, readsPolicy)
	out, errOut, err := runReads(t, f.dir, "ent", "show", "binary_sensor.door")
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("ent show of a denied entity: exit %d class %q (%v)\n%s", cliExitCode(err), policyErrorClass(err), err, out)
	}
	if want := `read entity:binary_sensor.door — deny "entity:binary_sensor.*" [read] (line 6)`; !strings.Contains(err.Error(), want) {
		t.Errorf("refusal %q does not name %q", err.Error(), want)
	}
	if strings.Contains(out, "binary_sensor.door") || strings.Contains(out+errOut, "hidden by policy") {
		t.Errorf("a refused read printed something of it, or a trailer:\nstdout %q\nstderr %q", out, errOut)
	}
}

// TestTplEvalNeedsSystemTemplate — D-143: while the profile denies a read,
// `tpl eval` needs `read system:template` allowed explicitly; with it, or
// under a profile that denies no read, it runs.
func TestTplEvalNeedsSystemTemplate(t *testing.T) {
	f := buildContractFixture(t)
	writePolicyFile(t, f.dir, readsPolicy)
	_, _, err := runReads(t, f.dir, "tpl", "eval", "{{ 1 + 1 }}")
	if policyErrorClass(err) != enforce.ClassDenied || !strings.Contains(err.Error(), "read system:template") {
		t.Fatalf("tpl eval under a read deny: %v, want refused on read system:template", err)
	}
	writePolicyFile(t, f.dir, readsPolicy+"    allow:\n      \"system:template\": [read]\n")
	if out, _, err := runReads(t, f.dir, "tpl", "eval", "{{ 1 + 1 }}"); err != nil || !strings.Contains(out, "2") {
		t.Errorf("tpl eval with system:template allowed: %v\n%s", err, out)
	}
	writePolicyFile(t, f.dir, "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [control]\n")
	if out, _, err := runReads(t, f.dir, "tpl", "eval", "{{ 1 + 1 }}"); err != nil || !strings.Contains(out, "2") {
		t.Errorf("tpl eval under a profile denying no read: %v\n%s", err, out)
	}
}

// TestReadsUnchangedWithoutAReadDeny — D-58 for reads: with no policy file
// every read prints what it printed before W-2; a profile that hides and masks
// nothing prints the same bytes and no trailer.
func TestReadsUnchangedWithoutAReadDeny(t *testing.T) {
	f := buildContractFixture(t)
	for _, args := range [][]string{{"ent", "ls"}, {"ent", "ls", "--json"}, {"area", "ls"}, {"dash", "ls"}, {"auto", "ls"}} {
		_ = os.Remove(filepath.Join(f.dir, policy.FileName))
		before, beforeErr, err := runReads(t, f.dir, args...)
		if err != nil {
			t.Fatalf("%v without a policy: %v", args, err)
		}
		writePolicyFile(t, f.dir, "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [control]\n")
		after, afterErr, err := runReads(t, f.dir, args...)
		if err != nil {
			t.Fatalf("%v under a profile denying no read: %v", args, err)
		}
		if before != after || beforeErr != afterErr {
			t.Errorf("%v: a profile that hides nothing changed the output:\n--- without\n%s%s\n--- with\n%s%s", args, before, beforeErr, after, afterErr)
		}
	}
}

// TestMCPCarriesTheTrailerBesideTheAnswer — over MCP a --json answer keeps
// its shape in its own content item, and the trailer is a second one.
func TestMCPCarriesTheTrailerBesideTheAnswer(t *testing.T) {
	f := buildContractFixture(t)
	writePolicyFile(t, f.dir, readsPolicy)
	srv := mcpserver.NewServer(mcpserver.Options{Runner: RunWithOutputContext, ResolvePath: FindCommandPath, Dir: f.dir, NoManualInject: true})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Wait() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": "ent ls --json"}})
	if err != nil || res.IsError || len(res.Content) != 2 {
		t.Fatalf("ent ls --json over MCP: %v, error %v, %d content items, want 2", err, res != nil && res.IsError, len(res.Content))
	}
	answer, _ := res.Content[0].(*mcp.TextContent)
	trailer, _ := res.Content[1].(*mcp.TextContent)
	var rows []any
	if answer == nil || json.Unmarshal([]byte(answer.Text), &rows) != nil {
		t.Errorf("the answer item does not parse as the --json array: %v", answer)
	}
	if trailer == nil || trailer.Text != "hidden by policy: 1, masked: 1 (profile agent)\n" {
		t.Errorf("the second item %v, want the trailer", trailer)
	}
}
