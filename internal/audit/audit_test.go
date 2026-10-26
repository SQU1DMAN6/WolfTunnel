package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizeURL(t *testing.T) {
	got := SanitizeURL("https://user:password@example.com/path?token=secret&x=1#fragment")
	want := "https://example.com/path"
	if got != want {
		t.Fatalf("SanitizeURL() = %q, want %q", got, want)
	}
}

func TestRedactionRotationRetentionAndPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l, err := New(dir, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "wolfnode-2000-01-01T00.jsonl")
	if err = os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Event: "http_fetch", Target: "https://user:password@example.com/private?token=secret#fragment", ID: "safe", Status: "200"})
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("retention did not remove old file")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(paths) != 1 {
		t.Fatalf("files %v", paths)
	}
	b, _ := os.ReadFile(paths[0])
	for _, secret := range []string{"user", "password", "/private", "token", "secret", "fragment"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("logged sensitive value %s", secret)
		}
	}
	info, _ := os.Stat(paths[0])
	if info.Mode().Perm() != 0600 {
		t.Fatal("log permissions")
	}
	if err = os.WriteFile(paths[0], make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	l.Log(Entry{Event: "rotate"})
	paths, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(paths) != 2 {
		t.Fatal("size rotation failed")
	}
	for i := 0; i < 70; i++ {
		p := filepath.Join(dir, fmt.Sprintf("wolfnode-2001-01-01T00-%03d.jsonl", i))
		if err = os.WriteFile(p, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	l.Purge()
	paths, _ = filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(paths) > 64 {
		t.Fatal("unbounded log count")
	}
	if err = os.Chmod(paths[0], 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = New(dir, time.Hour); err == nil {
		t.Fatal("insecure log accepted")
	}
}

func TestStableClientLogRotationAndSymlinkRejection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	l, e := NewClient(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	l.Log(Entry{Event: "connection_failed", Code: "NODE_UNREACHABLE", Error: "Connection refused"})
	p := filepath.Join(dir, "client.log")
	data, e := os.ReadFile(p)
	if e != nil || !strings.Contains(string(data), "connection_failed") || !strings.Contains(string(data), `"time"`) {
		t.Fatal("stable log missing", e)
	}
	os.WriteFile(p, make([]byte, 1<<20), 0600)
	l.Log(Entry{Event: "rotated"})
	if _, e = os.Stat(p + ".1"); e != nil {
		t.Fatal("stable log did not rotate", e)
	}
	os.Remove(p)
	os.Symlink("elsewhere", p)
	if _, e = NewClient(dir, time.Hour); e == nil {
		t.Fatal("client log symlink accepted")
	}
}

func TestStableNodeLogIncludesDiagnosticAndRejectsSymlinks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	logger, e := New(dir, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(dir, "node.log")
	logger.Log(Entry{Event: "browser_start_failed", Code: "BROWSER_NAMESPACE_DENIED", Stage: "cdp_version", Error: "Namespace denied", Remediation: "Check host profiles", Evidence: "Namespace creation failed", ExitStatus: "exit code 1"})
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	for _, text := range []string{"browser_start_failed", "cdp_version", "remediation", "evidence", "exit_status", "\"time\""} {
		if !strings.Contains(string(data), text) {
			t.Fatal("missing diagnostic field", text)
		}
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("node.log permissions")
	}
	os.WriteFile(p, make([]byte, 1<<20), 0600)
	logger.Log(Entry{Event: "rotate"})
	if _, e = os.Stat(p + ".1"); e != nil {
		t.Fatal(e)
	}
	os.Remove(p)
	os.Symlink("elsewhere", p)
	if _, e = New(dir, time.Hour); e == nil {
		t.Fatal("node log symlink accepted")
	}
}
