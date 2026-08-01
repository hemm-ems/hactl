package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// runThroughExecute drives the REAL entry point — Execute(), os.Args, os.Stdout
// — and returns what a caller of the binary would see on stdout, plus the error
// Execute returned.
//
// Every other test in this package calls the command function with its own
// buffer, and the integration tier goes through RunWithOutputContext. Neither
// can observe what Execute() does with the buffer AFTER the command body
// returns, which is where the answer was being dropped: Execute() renders into
// capBuf and used to flush it only on the success path, so a command that
// printed its report and then returned a verdict error printed nothing at all.
// A test can only see that from out here.
func runThroughExecute(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	oldOut, oldArgs := os.Stdout, os.Args
	os.Stdout, os.Args = w, append([]string{"hactl"}, args...)
	rootCmd.SetArgs(args)

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	err = Execute()

	_ = w.Close()
	os.Stdout, os.Args = oldOut, oldArgs
	rootCmd.SetArgs(nil)
	resetSubcommandFlags()
	out := <-done
	_ = r.Close()
	return out, err
}

// TestExecutePrintsTheAnswerItRenderedBeforeAVerdictError is the entry point's
// half of H-11's #36: `ref validate --exit-code` renders the dangling-reference
// table and then returns a sentinel error whose only job is the exit code. The
// unit test one layer down asserts the table reaches the writer — and it does;
// Execute() then threw the writer away, so on a real instance with 429 dangling
// references the entire answer was one mislabeled line on stderr.
//
// The rule this pins: an error ENDS a command, it does not ERASE what the
// command already printed. A refusal still prints nothing, because a refusal
// refuses before it renders — that is a property of the refusing code, not
// something the entry point has to enforce by deleting output.
func TestExecutePrintsTheAnswerItRenderedBeforeAVerdictError(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"}
	]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)

	out, err := runThroughExecute(t, "ref", "validate", "--exit-code", "--dir", ts.dir, "--tokensmax", "0")

	var ec interface{ ExitCode() int }
	if err == nil {
		t.Fatalf("--exit-code with a dangling reference must return a non-zero verdict; stdout was:\n%s", out)
	}
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("expected ExitCode()==1, got %v", err)
	}
	if !strings.Contains(out, "sensor.gone") {
		t.Fatalf("the report Execute() had already rendered never reached stdout:\n%q", out)
	}
}

// TestExecutePrintsNothingWhenTheCommandRefusesBeforeRendering is the control
// for the test above, and it is the promise the manual makes ("bad input is
// refused, not absorbed: exit 1, stderr, empty stdout"). Flushing the buffer on
// the error path may not turn a refusal into a half-answer — and it does not,
// because a refusal has written nothing to flush.
func TestExecutePrintsNothingWhenTheCommandRefusesBeforeRendering(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, nil)

	out, err := runThroughExecute(t, "ent", "show", "", "--dir", ts.dir)
	if err == nil {
		t.Fatal("a blank identifier must be refused (H-22)")
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("a refusal must leave stdout empty, got:\n%q", out)
	}
}
