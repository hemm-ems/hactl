//go:build !windows

package cmd

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hemm-ems/hactl/internal/policy"
)

// readUntil reads from r until want appears or the deadline passes, and
// returns everything read.
func readUntil(t *testing.T, r io.Reader, want string, d time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	var buf bytes.Buffer
	go func() {
		br := bufio.NewReader(r)
		for {
			b, err := br.ReadByte()
			if err != nil {
				got <- buf.String()
				return
			}
			buf.WriteByte(b)
			if strings.Contains(buf.String(), want) {
				got <- buf.String()
				return
			}
		}
	}()
	select {
	case s := <-got:
		return s
	case <-time.After(d):
		t.Fatalf("waited %s for %q; read so far:\n%s", d, want, buf.String())
		return ""
	}
}

// D-149: with a terminal on stdin and stdout — a real pseudo-terminal, as the
// owner's — activate shows the review, takes the typed profile name and
// writes the draft. The same process without one is refused
// (TestPolicyActivateRefusedWithoutATerminalCLI).
func TestPolicyActivateThroughATerminal(t *testing.T) {
	ts := policyRig(t, starterPolicy)
	if err := os.WriteFile(filepath.Join(ts.dir, "policy.draft.yaml"), []byte(activateDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	c := hactlProcess(t, "--dir", ts.dir, "policy", "activate", "policy.draft.yaml")
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Fatalf("starting under a pseudo-terminal: %v", err)
	}
	defer func() { _ = ptmx.Close() }()

	out := readUntil(t, ptmx, "anything else cancels: ", 20*time.Second)
	if !strings.Contains(out, "review: ") || !strings.Contains(out, "switch: 2 of 8") {
		t.Errorf("no review before the prompt:\n%s", out)
	}
	if _, err := ptmx.WriteString("agent\r"); err != nil {
		t.Fatal(err)
	}
	rest := readUntil(t, ptmx, "is in effect from the next command", 20*time.Second)
	if err := c.Wait(); err != nil {
		t.Fatalf("activate through a terminal: %v\n%s%s", err, out, rest)
	}
	if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != activateDraft {
		t.Errorf("policy.yaml is not the draft:\n%s", got)
	}
	if got := readFile(t, filepath.Join(ts.dir, policy.FileName+".prev")); got != starterPolicy {
		t.Error("policy.yaml.prev is not the starter")
	}
}

// D-149: one terminal is not enough — stdin AND stdout must be one. A child
// with a terminal on only one of them is refused (this is what tells the gate
// from `||`, or from a check of one stream).
func TestPolicyActivateNeedsATerminalOnBothStdinAndStdout(t *testing.T) {
	for _, half := range []string{"stdin only", "stdout only"} {
		t.Run(half, func(t *testing.T) {
			ts := policyRig(t, starterPolicy)
			if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
				t.Fatal(err)
			}
			ptmx, tty, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ptmx.Close(); _ = tty.Close() }()
			go func() { _, _ = io.Copy(io.Discard, ptmx) }()
			c := hactlProcess(t, "--dir", ts.dir, "policy", "activate", "d.yaml")
			var stderr bytes.Buffer
			c.Stderr = &stderr
			if half == "stdin only" {
				c.Stdin, c.Stdout = tty, io.Discard
			} else {
				c.Stdin, c.Stdout = strings.NewReader("agent\n"), tty
			}
			err = c.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "needs a terminal") {
				t.Errorf("a terminal on %s: %v\n%s", half, err, stderr.String())
			}
			if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != starterPolicy {
				t.Errorf("a terminal on %s: policy.yaml changed", half)
			}
		})
	}
}
