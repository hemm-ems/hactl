package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"version"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("version command failed: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("version command produced no output")
	}
	if !strings.Contains(out, "hactl") {
		t.Errorf("version output missing 'hactl': got %q", out)
	}
}

func TestRootCommandHelp(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"--help"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("help command failed: %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Fatal("help produced no output")
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		bytes int64
		want  int64
	}{
		{0, 0},
		{4, 1},
		{100, 25},
		{1000, 250},
		{3, 1},
		{5, 2},
	}
	for _, tt := range tests {
		got := estimateTokens(tt.bytes)
		if got != tt.want {
			t.Errorf("estimateTokens(%d) = %d, want %d", tt.bytes, got, tt.want)
		}
	}
}

func TestStatsWriter(t *testing.T) {
	var inner bytes.Buffer
	sw := &statsWriter{inner: &inner}

	n, err := sw.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 5 {
		t.Errorf("Write returned %d, want 5", n)
	}
	if sw.bytes != 5 {
		t.Errorf("bytes = %d, want 5", sw.bytes)
	}

	_, _ = sw.Write([]byte(" world"))
	if sw.bytes != 11 {
		t.Errorf("bytes = %d, want 11", sw.bytes)
	}
}

func TestWriteStats(t *testing.T) {
	var buf bytes.Buffer
	writeStats(&buf, 100)
	out := buf.String()
	if !strings.Contains(out, "100 bytes") {
		t.Errorf("stats output missing byte count: %s", out)
	}
	if !strings.Contains(out, "~25 tokens") {
		t.Errorf("stats output missing token count: %s", out)
	}
}

func TestApplyTokenPolicy_Header(t *testing.T) {
	flagTokensMax = 0 // no cap
	flagJSON = false
	defer func() { flagTokensMax = 500; flagJSON = false }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte("hello world"), "hactl version")
	out := buf.String()
	if !strings.HasPrefix(out, "[~") {
		t.Errorf("expected token header prefix, got: %q", out)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected full content in output, got: %q", out)
	}
}

func TestApplyTokenPolicy_JSON_NoHeader(t *testing.T) {
	flagTokensMax = 500
	flagJSON = true
	defer func() { flagTokensMax = 500; flagJSON = false }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte(`{"key":"value"}`), "hactl health")
	out := buf.String()
	if strings.Contains(out, "[~") {
		t.Errorf("JSON mode should not add token header, got: %q", out)
	}
	if out != `{"key":"value"}` {
		t.Errorf("JSON mode should pass through raw data, got: %q", out)
	}
}

func TestApplyTokenPolicy_Truncation(t *testing.T) {
	flagTokensMax = 1 // 1 token = 4 bytes cap
	defer func() { flagTokensMax = 500 }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte("hello world this is long"), "hactl version")
	out := buf.String()
	if !strings.Contains(out, "capped at 1 tok") {
		t.Errorf("expected truncation hint, got: %q", out)
	}
}

func TestApplyTokenPolicy_NoTruncation(t *testing.T) {
	flagTokensMax = 500
	defer func() { flagTokensMax = 500 }()

	var buf bytes.Buffer
	data := []byte("short output")
	applyTokenPolicy(&buf, data, "hactl version")
	out := buf.String()
	if strings.Contains(out, "capped") {
		t.Errorf("unexpected truncation on small output, got: %q", out)
	}
	if !strings.Contains(out, "short output") {
		t.Errorf("expected full content in output, got: %q", out)
	}
}

func TestApplyTokenPolicy_HintLog(t *testing.T) {
	flagTokensMax = 1
	defer func() { flagTokensMax = 500 }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte("a]long log output here"), "hactl log")
	out := buf.String()
	if !strings.Contains(out, "--component") {
		t.Errorf("log hint should suggest --component, got: %q", out)
	}
}

func TestApplyTokenPolicy_HintEntLs(t *testing.T) {
	flagTokensMax = 1
	defer func() { flagTokensMax = 500 }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte("lots of entities here"), "hactl ent ls")
	out := buf.String()
	if !strings.Contains(out, "--domain") {
		t.Errorf("ent ls hint should suggest --domain, got: %q", out)
	}
}

func TestApplyTokenPolicy_HintAutoLs(t *testing.T) {
	flagTokensMax = 1
	defer func() { flagTokensMax = 500 }()

	var buf bytes.Buffer
	applyTokenPolicy(&buf, []byte("lots of automations"), "hactl auto ls")
	out := buf.String()
	if !strings.Contains(out, "--pattern") {
		t.Errorf("auto ls hint should suggest --pattern, got: %q", out)
	}
}

func TestApplyTokenPolicy_UTF8Safety(t *testing.T) {
	flagTokensMax = 1 // 1 token = 4 bytes cap
	defer func() { flagTokensMax = 500 }()

	// "€" is 3 bytes (0xE2 0x82 0xAC). Build a string where the naive
	// 4-byte limit would split mid-rune: "€€" = 6 bytes, limit=4 would
	// split the second euro sign.
	data := []byte("€€extra")
	var buf bytes.Buffer
	applyTokenPolicy(&buf, data, "hactl version")
	out := buf.String()
	// The truncated portion should be valid UTF-8 (just "€" = 3 bytes)
	if strings.Contains(out, "\ufffd") {
		t.Errorf("truncation produced invalid UTF-8 replacement char in: %q", out)
	}
	// Should still contain the truncation hint
	if !strings.Contains(out, "capped at 1 tok") {
		t.Errorf("expected truncation hint, got: %q", out)
	}
}
