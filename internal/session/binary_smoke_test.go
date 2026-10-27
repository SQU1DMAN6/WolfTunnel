package session_test

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercises distributed executables, a fresh managed renderer configuration,
// invitation import and encrypted pixels. It requires no external website.
func TestBuiltBinaryBrowserSmoke(t *testing.T) {
	nodeBin, clientBin, engine := os.Getenv("WOLF_NODE_BIN"), os.Getenv("WOLF_CLIENT_BIN"), os.Getenv("WOLF_BROWSER_BIN")
	if nodeBin == "" || clientBin == "" || engine == "" {
		t.Skip("built-binary browser-smoke target required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root := t.TempDir()
	nodeState, clientState := filepath.Join(root, "node"), filepath.Join(root, "client")
	stderr, e := os.Create(filepath.Join(root, "node.stderr"))
	if e != nil {
		t.Fatal(e)
	}
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, nodeBin, "-state-dir", nodeState, "-listen", "127.0.0.1:0", "-browser-bin", engine, "-browser-sha256", browserHash(t, engine))
	cmd.Stderr = stderr
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			if _, address, ok := strings.Cut(line, "node ready on "); ok {
				ready <- address
				break
			}
		}
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cmd.Process.Signal(os.Interrupt)
		if e := cmd.Wait(); e != nil {
			t.Errorf("node shutdown: %v", e)
		}
	}
	defer stop()
	var address string
	select {
	case address = <-ready:
	case <-time.After(8 * time.Second):
		t.Fatal("bounded node readiness timeout")
	}
	run := func(bin string, args ...string) []byte {
		t.Helper()
		call, stop := context.WithTimeout(ctx, 25*time.Second)
		defer stop()
		command := exec.CommandContext(call, bin, args...)
		command.Env = append(os.Environ(), "SDL_VIDEODRIVER=dummy", "SDL_RENDER_DRIVER=software")
		out, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("built binary failed: %v %s", e, out)
		}
		return out
	}
	invitation := bytes.TrimSpace(run(nodeBin, "-state-dir", nodeState, "-issue-enrolment", "-invite-address", address))
	private := filepath.Join(root, "invitation")
	if e = os.WriteFile(private, invitation, 0600); e != nil {
		t.Fatal(e)
	}
	run(clientBin, "-state-dir", clientState, "-invite-file", private)
	dir := os.Getenv("WOLF_ARTIFACT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	os.MkdirAll(dir, 0700)
	capture := filepath.Join(dir, "binary-blank.png")
	out := run(clientBin, "-state-dir", clientState, "-capture-blank", "-output", capture, "-smoke-timeout", "20s")
	if !bytes.Contains(out, []byte("Remote pixels captured: 1920x1080")) {
		t.Fatal("unexpected pixel result", string(out))
	}
	// An explicitly empty proxy flag must clear a saved proxy when returning to
	// direct mode; merely checking its string value loses that user instruction.
	run(clientBin, "-state-dir", clientState, "-transport", "http-connect", "-proxy", "http://127.0.0.1:1", "-retries", "0", "-smoke-test")
	run(clientBin, "-state-dir", clientState, "-transport", "tls-tcp", "-proxy", "", "-test-connection")
	stop()
	data, e := os.ReadFile(filepath.Join(nodeState, "logs", "node.log"))
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(data, []byte("browser_session_closed")) || bytes.Contains(data, invitation) {
		t.Fatal("teardown/log redaction failure")
	}
	os.WriteFile(filepath.Join(dir, "binary-node.log"), data, 0600)
	t.Logf("built node/client: loopback=%s; pinned invitation; blank 1920x1080 frame; clean teardown", address)
}
