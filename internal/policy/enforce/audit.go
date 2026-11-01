package enforce

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AuditFileName is the audit log's name under `<instance>/cache/` (D-71).
const AuditFileName = "policy-audit.log"

// AuditRotateBytes is the size at which the log is renamed to
// `policy-audit.log.1`, replacing the previous generation (D-71: 10 MiB, one
// generation). A variable so a test can bound it.
var AuditRotateBytes int64 = 10 << 20

// AuditLine is one JSON line of the audit log (D-71): one write-verb decision,
// allowed or denied, preview or confirmed run.
type AuditLine struct {
	Time string `json:"time"`
	// Command is the command path and its positional arguments; flag values
	// never appear, so content-carrying flags (`--data`, `-f`) are never
	// logged.
	Command string `json:"command"`
	DryRun  bool   `json:"dry_run"`
	// Layer is LayerCommand or LayerRequest.
	Layer    string `json:"layer"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Allowed  bool   `json:"allowed"`
	Rule     string `json:"rule"`
	Line     int    `json:"line"`
	Profile  string `json:"profile"`
}

// Audit appends to `<instance>/cache/policy-audit.log`. hactl never reads it
// back; it is the owner's.
type Audit struct {
	path string
}

// NewAudit is the audit log of the instance at dir.
func NewAudit(dir string) *Audit {
	return &Audit{path: filepath.Join(dir, "cache", AuditFileName)}
}

// Path is the log's path.
func (a *Audit) Path() string { return a.path }

// Append writes lines in one write, rotating first when the file has reached
// AuditRotateBytes. Any failure — the cache directory cannot be created, the
// file cannot be opened, rotated or written — is returned: the caller refuses
// the write the lines record (D-71, `fail-closed: audit log not writable`).
func (a *Audit) Append(lines []AuditLine) error {
	if len(lines) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return fmt.Errorf("encoding audit line: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(a.path), 0o750); err != nil {
		return fmt.Errorf("audit log %s: %w", a.path, err)
	}
	if err := a.rotate(); err != nil {
		return err
	}
	// O_APPEND without O_TRUNC: lines are only ever added, whoever else holds
	// the file (H-26).
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit log %s: %w", a.path, err)
	}
	_, werr := f.Write(buf.Bytes())
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("audit log %s: %w", a.path, werr)
	}
	if cerr != nil {
		return fmt.Errorf("audit log %s: %w", a.path, cerr)
	}
	return nil
}

// rotate renames a log that has reached AuditRotateBytes to `.1`, replacing
// the previous generation.
func (a *Audit) rotate() error {
	fi, err := os.Stat(a.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("audit log %s: %w", a.path, err)
	case fi.Size() < AuditRotateBytes:
		return nil
	}
	if err := os.Rename(a.path, a.path+".1"); err != nil {
		return fmt.Errorf("rotating audit log %s: %w", a.path, err)
	}
	return nil
}
