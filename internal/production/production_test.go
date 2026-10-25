// Package production verifies the same built binaries/defaults used by operators.
package production

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalNodeManagedRendererPublicWWW(t *testing.T) {
	nodeBin, clientBin, url := os.Getenv("WOLF_NODE_BIN"), os.Getenv("WOLF_CLIENT_BIN"), os.Getenv("WOLF_PUBLIC_TEST_URL")
	if nodeBin == "" || clientBin == "" || url == "" {
		t.Skip("production-smoke target enables the actual local binaries and public URL")
	}
	artifacts := os.Getenv("WOLF_ARTIFACT_DIR")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	if e := os.MkdirAll(artifacts, 0700); e != nil {
		t.Fatal(e)
	}
	temporary, e := os.MkdirTemp("", "wolf-production-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(temporary)
	nodeData, clientData := filepath.Join(temporary, "node-data"), filepath.Join(temporary, "client-data")
	nodeEnv := append(os.Environ(), "XDG_DATA_HOME="+nodeData)
	clientEnv := append(os.Environ(), "XDG_DATA_HOME="+clientData)
	// Use the real display when available; CI can exercise the identical SDL path
	// with the software driver. No externally reachable node is contacted.
	if os.Getenv("WOLF_NATIVE_DISPLAY") != "1" {
		clientEnv = append(clientEnv, "SDL_VIDEODRIVER=dummy", "SDL_RENDER_DRIVER=software")
	}
	var nodeOutput strings.Builder
	var logMu sync.Mutex
	launch := func(args ...string) func() {
		ctx, cancel := context.WithCancel(context.Background())
		command := exec.CommandContext(ctx, nodeBin, args...)
		command.Env = nodeEnv
		stdout, e := command.StdoutPipe()
		if e != nil {
			t.Fatal(e)
		}
		stderr := &bytes.Buffer{}
		command.Stderr = stderr
		if e = command.Start(); e != nil {
			t.Fatal(e)
		}
		ready := make(chan struct{})
		readDone := make(chan struct{})
		go func() {
			defer close(readDone)
			scan := bufio.NewScanner(stdout)
			signaled := false
			for scan.Scan() {
				line := scan.Text()
				logMu.Lock()
				nodeOutput.WriteString(line + "\n")
				logMu.Unlock()
				if strings.Contains(line, "node ready on 127.0.0.1:13300") && !signaled {
					close(ready)
					signaled = true
				}
			}
		}()
		select {
		case <-ready:
		case <-time.After(15 * time.Second):
			cancel()
			command.Wait()
			<-readDone
			t.Fatalf("local node readiness failed (port 13300 must be free): %s", stderr.String())
		}
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			command.Process.Signal(os.Interrupt)
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			select {
			case e := <-done:
				if e != nil {
					t.Error("node did not exit cleanly", e)
				}
			case <-time.After(10 * time.Second):
				cancel()
				<-done
				t.Error("node shutdown timeout")
			}
			cancel()
			<-readDone
			logMu.Lock()
			nodeOutput.WriteString(stderr.String())
			logMu.Unlock()
		}
		t.Cleanup(stop)
		return stop
	}
	run := func(environment []string, args ...string) string {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Env = environment
		output, e := command.CombinedOutput()
		if e != nil {
			t.Fatalf("built command failed: %s", output)
		}
		return string(output)
	}
	stop := launch("-listen", "127.0.0.1:13300")
	nodeRoot := filepath.Join(nodeData, "wolftunnel", "node")
	cfg, e := os.ReadFile(filepath.Join(nodeRoot, "browser.json"))
	if e != nil {
		t.Fatal(e)
	}
	var browser struct {
		Binary string `json:"binary"`
		Hash   string `json:"sha256"`
	}
	if e = json.Unmarshal(cfg, &browser); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(browser.Binary, "/renderer/") || strings.HasPrefix(browser.Binary, "/usr/") || strings.HasPrefix(browser.Binary, "/opt/") {
		t.Fatal("test did not use the application-managed renderer")
	}
	invitation := strings.TrimSpace(run(nodeEnv, nodeBin, "-issue-enrolment"))
	if !strings.HasPrefix(invitation, "wolftunnel://127.0.0.1:13300/enrol?") {
		t.Fatal("default enrolment output is not a usable complete invitation")
	}
	invitationPath := filepath.Join(temporary, "client.invite")
	if e = os.WriteFile(invitationPath, []byte(invitation), 0600); e != nil {
		t.Fatal(e)
	}
	screenshot := filepath.Join(artifacts, "public-native.bmp")
	output := run(clientEnv, clientBin, "-invite-file", invitationPath, "-open", url, "-screenshot", screenshot, "-interaction-smoke", "-smoke-timeout", "40s")
	t.Log(strings.TrimSpace(output))
	if !strings.Contains(output, "status=200") {
		t.Fatal("public page did not return HTTP 200")
	}
	ackMatch := regexp.MustCompile(`input_acks=(\d+)`).FindStringSubmatch(output)
	acks := 0
	if len(ackMatch) == 2 {
		acks, _ = strconv.Atoi(ackMatch[1])
	}
	if acks < 4 {
		t.Fatal("native mouse and physical keyboard events were not acknowledged")
	}
	if e = os.WriteFile(filepath.Join(artifacts, "production-client-output.log"), []byte(output), 0600); e != nil {
		t.Fatal(e)
	}
	clientLogPath := filepath.Join(clientData, "wolftunnel", "logs", "client.log")
	logs, e := os.ReadFile(clientLogPath)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(logs, []byte(invitation)) || bytes.Contains(logs, []byte(url)) || bytes.Contains(logs, []byte("\"signature\"")) {
		t.Fatal("secret or browsing content leaked to client diagnostics")
	}
	if !bytes.Contains(logs, []byte("enrolment_result")) || !bytes.Contains(logs, []byte("browser_open_result")) {
		t.Fatal("connection/browser stages absent from client.log")
	}
	// Both binaries reuse state/preferences without state/address/browser flags.
	if output = run(clientEnv, clientBin, "-test-connection"); !strings.Contains(output, "Connected") {
		t.Fatal("saved client credentials were not reused")
	}
	stop()
	stop = launch() // saved loopback listener and managed browser configuration
	if output = run(clientEnv, clientBin, "-capture", url, "-output", filepath.Join(artifacts, "public-after-restart.png"), "-smoke-timeout", "30s"); !strings.Contains(output, "Remote pixels captured") {
		t.Fatal("renderer/defaults did not survive a zero-flag node restart")
	}
	stop()
	logs, e = os.ReadFile(clientLogPath)
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(artifacts, "client.log"), logs, 0600)
	logMu.Lock()
	os.WriteFile(filepath.Join(artifacts, "production-node.log"), []byte(nodeOutput.String()), 0600)
	logMu.Unlock()
	entries, e := os.ReadDir(filepath.Join(nodeRoot, "logs"))
	if e != nil {
		t.Fatal(e)
	}
	var audit bytes.Buffer
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			f, e := os.Open(filepath.Join(nodeRoot, "logs", entry.Name()))
			if e != nil {
				t.Fatal(e)
			}
			io.Copy(&audit, f)
			f.Close()
		}
	}
	if !bytes.Contains(audit.Bytes(), []byte("browser_session_closed")) {
		t.Fatal("browser cleanup was not recorded")
	}
	os.WriteFile(filepath.Join(artifacts, "node-audit.jsonl"), audit.Bytes(), 0600)
	t.Logf("Managed runtime %s; default port 13300; native public pixels/input, saved credentials and zero-flag node restart passed", browser.Hash)
}

func TestStandaloneNodeFirstRunDownloadsItsOwnRenderer(t *testing.T) {
	if os.Getenv("WOLF_DOWNLOAD_TEST") != "1" {
		t.Skip("explicit fresh-download validation")
	}
	nodeBin, clientBin := os.Getenv("WOLF_NODE_BIN"), os.Getenv("WOLF_CLIENT_BIN")
	if nodeBin == "" || clientBin == "" {
		t.Fatal("built binaries required")
	}
	temporary, e := os.MkdirTemp("", "wolf-standalone-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(temporary)
	standalone := filepath.Join(temporary, "wolfnode")
	source, e := os.Open(nodeBin)
	if e != nil {
		t.Fatal(e)
	}
	target, e := os.OpenFile(standalone, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(target, source)
	source.Close()
	target.Close()
	if e != nil {
		t.Fatal(e)
	}
	nodeData, clientData := filepath.Join(temporary, "node-data"), filepath.Join(temporary, "client-data")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	nodeEnv := append(os.Environ(), "XDG_DATA_HOME="+nodeData)
	clientEnv := append(os.Environ(), "XDG_DATA_HOME="+clientData)
	command := exec.CommandContext(ctx, standalone, "-listen", "127.0.0.1:13300")
	command.Env = nodeEnv
	stdout, e := command.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	start := time.Now()
	if e = command.Start(); e != nil {
		t.Fatal(e)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		command.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Error("standalone shutdown timeout")
		}
	}
	defer stop()
	ready := make(chan struct{})
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "node ready on 127.0.0.1:13300") {
				close(ready)
				break
			}
		}
		io.Copy(io.Discard, stdout)
	}()
	select {
	case <-ready:
	case <-readDone:
		stop()
		t.Fatalf("fresh managed start failed: %s", stderr.String())
	case <-ctx.Done():
		t.Fatal("fresh renderer download/start timeout")
	}
	duration := time.Since(start)
	run := func(environment []string, args ...string) string {
		c := exec.CommandContext(ctx, args[0], args[1:]...)
		c.Env = environment
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("standalone flow failed: %s", b)
		}
		return string(b)
	}
	inv := strings.TrimSpace(run(nodeEnv, standalone, "-issue-enrolment"))
	secret := filepath.Join(temporary, "invitation")
	if e = os.WriteFile(secret, []byte(inv), 0600); e != nil {
		t.Fatal(e)
	}
	run(clientEnv, clientBin, "-invite-file", secret)
	artifacts := os.Getenv("WOLF_ARTIFACT_DIR")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	output := run(clientEnv, clientBin, "-capture-blank", "-output", filepath.Join(artifacts, "standalone-pixels.png"), "-smoke-timeout", "30s")
	stop()
	<-readDone
	var cfg struct {
		Binary string `json:"binary"`
	}
	raw, e := os.ReadFile(filepath.Join(nodeData, "wolftunnel", "node", "browser.json"))
	if e != nil || json.Unmarshal(raw, &cfg) != nil || !strings.HasPrefix(cfg.Binary, nodeData) {
		t.Fatal("standalone used a system or colocated renderer")
	}
	report := map[string]any{"first_start_seconds": duration.Seconds(), "source": "official pinned runtime downloaded by standalone node", "system_browser_used": false, "result": strings.TrimSpace(output)}
	raw, e = json.MarshalIndent(report, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(artifacts, "standalone-download.json"), raw, 0600)
	t.Log(string(raw))
}
