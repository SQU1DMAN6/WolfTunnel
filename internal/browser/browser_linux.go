//go:build linux

// Package browser owns isolated Chromium processes and a fixed CDP adapter.
package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

type Config struct {
	MaxPerClient  int           `json:"max_per_client"`
	Binary        string        `json:"binary"`
	SHA256        string        `json:"sha256"`
	MaxSessions   int           `json:"max_sessions"`
	MaxTabs       int           `json:"max_tabs"`
	MemoryMB      int           `json:"memory_mb"`
	TotalMemoryMB int           `json:"total_memory_mb"`
	CPUPercent    int           `json:"cpu_percent"`
	Tasks         int           `json:"tasks"`
	TempMB        int           `json:"temp_mb"`
	MaxWidth      int           `json:"max_width"`
	MaxHeight     int           `json:"max_height"`
	MaxPixels     int           `json:"max_pixels"`
	FPS           int           `json:"fps"`
	Quality       int           `json:"quality"`
	Bandwidth     int           `json:"bandwidth"`
	MaxFrameBytes int           `json:"max_frame_bytes"`
	Refresh       time.Duration `json:"refresh"`
	Startup       time.Duration `json:"startup"`
	Navigation    time.Duration `json:"navigation"`
	Idle          time.Duration `json:"idle"`
}

func DefaultConfig() Config {
	return Config{MaxPerClient: 1, MaxSessions: 2, MaxTabs: 4, MemoryMB: 1024, TotalMemoryMB: 2048, CPUPercent: 150, Tasks: 384, TempMB: 192, MaxWidth: 1920, MaxHeight: 1200, MaxPixels: 1920 * 1200, FPS: 15, Quality: 82, Bandwidth: 4 << 20, MaxFrameBytes: v3.MaxPixel, Refresh: 2 * time.Second, Startup: 20 * time.Second, Navigation: 20 * time.Second, Idle: 2 * time.Minute}
}
func (c Config) Valid() bool {
	if c.MaxFrameBytes < 64<<10 || c.MaxFrameBytes > v3.MaxPixel || c.Refresh < 100*time.Millisecond || c.Refresh > 10*time.Second {
		return false
	}
	return c.MaxPerClient >= 1 && c.MaxPerClient <= c.MaxSessions && (c.Binary == "" || (filepath.IsAbs(c.Binary) && len(c.SHA256) == 64)) && c.MaxSessions >= 1 && c.MaxSessions <= 8 && c.MaxTabs >= 1 && c.MaxTabs <= 8 && c.MemoryMB >= 512 && c.MemoryMB <= 4096 && c.TotalMemoryMB >= c.MemoryMB && c.TotalMemoryMB <= 8192 && c.MaxSessions*c.MemoryMB <= c.TotalMemoryMB && c.CPUPercent >= 25 && c.CPUPercent <= 400 && c.Tasks >= 32 && c.Tasks <= 512 && c.TempMB >= 64 && c.TempMB <= 512 && c.MaxWidth >= 320 && c.MaxWidth <= 3840 && c.MaxHeight >= 200 && c.MaxHeight <= 2160 && c.MaxPixels >= 64000 && c.MaxPixels <= 3840*2160 && c.FPS >= 1 && c.FPS <= 30 && c.Quality >= 40 && c.Quality <= 95 && c.Bandwidth >= 64<<10 && c.Bandwidth <= 16<<20 && c.Startup >= time.Second && c.Startup <= time.Minute && c.Navigation >= time.Second && c.Navigation <= time.Minute && c.Idle >= 5*time.Second && c.Idle <= 30*time.Minute
}
func (c Config) Viewport(w, h int) bool {
	return w >= 320 && h >= 200 && w <= c.MaxWidth && h <= c.MaxHeight && int64(w)*int64(h) <= int64(c.MaxPixels)
}

type Page struct {
	ID, Target, CDP string
	MainFrame       string
	LinkFrame       string
	LinkWorld       int
}
type Event struct {
	Kind                            string
	Tab, State, Code, Title, Target string
	Status                          int
}
type Browser struct {
	terminalCode     string
	hoverEpoch       uint64
	navigation       bool
	interceptedClick bool
	hoverPoints      chan hoverPoint
	hoverTarget      string
	creating         int
	approved         map[string]bool
	potential        map[string]bool
	captureMu        sync.RWMutex
	Config           Config
	Proxy            *browsernet.Proxy
	cmd              *exec.Cmd
	cdp              *cdp
	scope            string
	done             chan struct{}
	exitError        error
	ctx              context.Context
	cancel           context.CancelFunc
	once             sync.Once
	mu               sync.Mutex
	pages            map[string]*Page
	active           string
	width, height    int
	contextID        string
	keys             map[string]v3.Input
	buttons          int
	Events           chan Event
}

func Start(parent context.Context, c Config, p *browsernet.Proxy) (*Browser, error) {
	return StartObserved(parent, c, p, nil)
}

// StartObserved records checkpoints before any web content is loaded. A failure
// is classified once and returned with fixed advice, not raw stderr or CDP text.
func StartObserved(parent context.Context, c Config, p *browsernet.Proxy, observe func(StartupDiagnostic)) (opened *Browser, result error) {
	started := time.Now()
	stage := "configuration"
	scope := ""
	checkpoint := func(next string) {
		stage = next
		if observe != nil {
			observe(StartupDiagnostic{Stage: stage, Status: "checking", Scope: scope, DurationMS: time.Since(started).Milliseconds()})
		}
	}
	defer func() {
		if result != nil {
			var failure *startupError
			if !errors.As(result, &failure) {
				result = StartFailure(stage, "BROWSER_START_FAILED", "Unexpected startup failure; raw error omitted.")
			}
			d := Diagnosis(result)
			d.Scope = scope
			d.DurationMS = time.Since(started).Milliseconds()
			if observe != nil {
				observe(d)
			}
		} else if observe != nil {
			observe(StartupDiagnostic{Stage: "ready", Status: "ready", Scope: scope, DurationMS: time.Since(started).Milliseconds()})
		}
	}()
	checkpoint("configuration")
	if !c.Valid() || c.Binary == "" {
		return nil, StartFailure(stage, "BROWSER_CONFIG_INVALID", "")
	}
	if observe != nil {
		observe(hostEnvironment())
	}
	checkpoint("node_account")
	if os.Geteuid() == 0 {
		return nil, StartFailure(stage, "BROWSER_ROOT_FORBIDDEN", "")
	}
	checkpoint("isolation_tools")
	for _, tool := range []string{"systemd-run", "systemctl"} {
		if _, e := exec.LookPath(tool); e != nil {
			return nil, StartFailure(stage, "BROWSER_TOOL_MISSING", "Unavailable tool: "+tool)
		}
	}
	launcher, launcherErr := selectLauncher()
	if launcherErr != nil {
		var typed *startupError
		if errors.As(launcherErr, &typed) {
			return nil, launcherErr
		}
		return nil, StartFailure(stage, "BROWSER_TOOL_MISSING", "Unavailable tool: bwrap")
	}
	checkpoint("launcher_policy")
	policy, policyErr := launcherPolicy(parent, launcher)
	if observe != nil {
		observe(policy)
	}
	if policyErr != nil {
		return nil, policyErr
	}
	checkpoint("renderer_integrity")
	file, e := os.Open(c.Binary)
	if e != nil {
		return nil, StartFailure(stage, "BROWSER_BINARY_INVALID", "Renderer is not a readable, private ELF executable.")
	}
	// Pin the engine, not a shell wrapper whose unchanged digest could launch a
	// different executable after a distribution update.
	info, statErr := file.Stat()
	var signature [4]byte
	_, signatureErr := io.ReadFull(file, signature[:])
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 || signatureErr != nil || signature != [4]byte{0x7f, 'E', 'L', 'F'} {
		file.Close()
		return nil, StartFailure(stage, "BROWSER_BINARY_INVALID", "Renderer is not a readable, private ELF executable.")
	}
	engine, elfError := elf.NewFile(file)
	if elfError != nil {
		file.Close()
		return nil, StartFailure(stage, "BROWSER_BINARY_INVALID", "Invalid ELF executable.")
	}
	expected := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if engine.Machine != expected || engine.Class != elf.ELFCLASS64 || engine.Data != elf.ELFDATA2LSB {
		file.Close()
		return nil, StartFailure(stage, "BROWSER_ARCH_MISMATCH", "ELF CPU architecture does not match the node.")
	}
	if _, e = file.Seek(0, io.SeekStart); e != nil {
		file.Close()
		return nil, StartFailure(stage, "BROWSER_BINARY_INVALID", "Renderer is not a readable, private ELF executable.")
	}
	h := sha256.New()
	_, e = io.Copy(h, file)
	file.Close()
	if e != nil || hex.EncodeToString(h.Sum(nil)) != strings.ToLower(c.SHA256) {
		return nil, StartFailure(stage, "BROWSER_PIN_MISMATCH", "")
	}
	exe, e := os.Executable()
	if e != nil {
		return nil, StartFailure("helper_executable", "BROWSER_WORKER_FAILED", "")
	}
	ctx, cancel := context.WithCancel(parent)
	b := &Browser{Config: c, Proxy: p, ctx: ctx, cancel: cancel, done: make(chan struct{}), pages: make(map[string]*Page), keys: make(map[string]v3.Input), Events: make(chan Event, 32), scope: "wolf-browser-" + v3.NewID().String() + ".scope"}
	scope = b.scope
	checkpoint("control_pipes")
	inputR, inputW, e := os.Pipe()
	if e != nil {
		cancel()
		return nil, StartFailure(stage, "BROWSER_IPC_FAILED", "")
	}
	outputR, outputW, e := os.Pipe()
	if e != nil {
		inputR.Close()
		inputW.Close()
		cancel()
		return nil, StartFailure(stage, "BROWSER_IPC_FAILED", "")
	}
	args := []string{"--user", "--scope", "--quiet", "--unit=" + b.scope, "-p", "MemoryMax=" + strconv.Itoa(c.MemoryMB) + "M", "-p", "MemorySwapMax=0", "-p", "TasksMax=" + strconv.Itoa(c.Tasks), "-p", "CPUQuota=" + strconv.Itoa(c.CPUPercent) + "%", "-p", "RuntimeMaxSec=1800", launcher, "--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-net", "--unshare-ipc", "--unshare-uts", "--cap-drop", "ALL", "--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib", "/lib64", "--proc", "/proc", "--dev", "/dev", "--dir", "/etc"}
	for _, path := range []string{"/etc/fonts", "/etc/ssl/certs", "/etc/passwd", "/etc/group", "/etc/nsswitch.conf", "/etc/ld.so.cache"} {
		if _, e := os.Stat(path); e == nil {
			args = append(args, "--ro-bind", path, path)
		}
	}
	args = append(args, "--ro-bind", filepath.Dir(c.Binary), "/browser", "--ro-bind", exe, "/wolf-helper", "--ro-bind", p.Dir, "/egress", "--size", strconv.Itoa(c.TempMB<<20), "--tmpfs", "/tmp", "--dir", "/home/wolf", "--size", "67108864", "--tmpfs", "/dev/shm", "--clearenv", "--setenv", "HOME", "/home/wolf", "--setenv", "PATH", "/usr/bin", "--setenv", "LANG", "C.UTF-8", "--chdir", "/tmp", "/wolf-helper", "__wolf_browser_worker", "/browser/"+filepath.Base(c.Binary))
	b.cmd = exec.CommandContext(ctx, "systemd-run", args...)
	b.cmd.ExtraFiles = []*os.File{inputR, outputW}
	b.cmd.Stdout = io.Discard
	diagnostic := &startupBuffer{enabled: true}
	b.cmd.Stderr = diagnostic
	checkpoint("scope_launch")
	if e = b.cmd.Start(); e != nil {
		inputR.Close()
		inputW.Close()
		outputR.Close()
		outputW.Close()
		cancel()
		return nil, StartFailure(stage, "BROWSER_IPC_FAILED", "Could not execute systemd-run; check its execution permissions and process/file limits.")
	}
	inputR.Close()
	outputW.Close()
	b.cdp = newCDP(outputR, inputW)
	go func() { b.exitError = b.cmd.Wait(); b.cdp.close(); close(b.done); b.cancel() }()
	startup, stop := context.WithTimeout(ctx, c.Startup)
	defer stop()
	fail := func(err error) (*Browser, error) {
		resource := b.startupScopeEvidence()
		status := "not exited before cleanup"
		select {
		case <-b.done:
			var exit *exec.ExitError
			if errors.As(b.exitError, &exit) {
				status = fmt.Sprintf("exit code %d", exit.ExitCode())
			} else if b.exitError == nil {
				status = "exit code 0"
			} else {
				status = "launcher wait failed"
			}
		default:
		}
		failure := classifyStartup(stage, diagnostic.String(), errors.Is(err, context.DeadlineExceeded), resource, err)
		var typed *startupError
		if errors.As(err, &typed) {
			failure = err
		}
		d := Diagnosis(failure)
		if engine := engineExit.FindStringSubmatch(diagnostic.String()); len(engine) == 3 {
			status += "; engine " + strings.ToLower(engine[1]) + " " + engine[2]
		}
		d.ExitStatus = status
		b.Close()
		return nil, &startupError{diagnostic: d}
	}
	checkpoint("cdp_version")
	var version struct {
		Product string `json:"product"`
	}
	if e = b.cdp.call(startup, "", "Browser.getVersion", map[string]any{}, &version); e != nil {
		return fail(e)
	}
	if !strings.Contains(version.Product, "Chrome") && !strings.Contains(version.Product, "Chromium") {
		return fail(StartFailure(stage, "BROWSER_CDP_FAILED", "Renderer did not identify as Chromium."))
	}
	checkpoint("cdp_context")
	var bc struct {
		ID string `json:"browserContextId"`
	}
	if e = b.cdp.call(startup, "", "Target.createBrowserContext", map[string]any{"disposeOnDetach": true}, &bc); e != nil {
		return fail(e)
	}
	b.contextID = bc.ID
	checkpoint("cdp_download_policy")
	if e = b.cdp.call(startup, "", "Browser.setDownloadBehavior", map[string]any{"behavior": "deny", "browserContextId": bc.ID}, nil); e != nil {
		return fail(e)
	}
	checkpoint("cdp_permissions")
	for _, permission := range []string{"microphone", "camera", "geolocation", "notifications", "clipboard-read", "clipboard-write"} {
		if e = b.cdp.call(startup, "", "Browser.setPermission", map[string]any{"permission": map[string]string{"name": permission}, "setting": "denied", "browserContextId": bc.ID}, nil); e != nil {
			return fail(e)
		}
	}
	checkpoint("sandbox_verification")
	if e = b.verifySandbox(startup); e != nil {
		return fail(e)
	}
	b.approved = make(map[string]bool)
	b.potential = make(map[string]bool)
	checkpoint("cdp_target_discovery")
	if e = b.cdp.call(startup, "", "Target.setDiscoverTargets", map[string]bool{"discover": true}, nil); e != nil {
		return fail(e)
	}

	diagnostic.disable()
	b.hoverPoints = make(chan hoverPoint, 1)
	go b.hover()
	go b.events()
	return b, nil
}

// Chromium's own renderer sandbox is verified in addition to the outer OS
// namespaces/cgroup. This fixed internal page is never exposed to clients.
func (b *Browser) verifySandbox(ctx context.Context) error {
	var target struct {
		ID string `json:"targetId"`
	}
	if e := b.cdp.call(ctx, "", "Target.createTarget", map[string]string{"url": "chrome://sandbox/"}, &target); e != nil {
		return e
	}
	defer b.cdp.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": target.ID}, nil)
	var attached struct {
		ID string `json:"sessionId"`
	}
	if e := b.cdp.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.ID, "flatten": true}, &attached); e != nil {
		return e
	}
	last := ""
	for {
		var result struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		e := b.cdp.call(ctx, attached.ID, "Runtime.evaluate", map[string]any{"expression": "document.body ? document.body.innerText : ''", "returnByValue": true}, &result)
		last = result.Result.Value
		if e == nil && regexp.MustCompile(`(?i)Seccomp-BPF sandbox\s+Yes`).MatchString(result.Result.Value) && regexp.MustCompile(`(?i)(Namespace Sandbox\s+Yes|Layer 1 Sandbox\s+Namespace)`).MatchString(result.Result.Value) {
			return nil
		}
		if e == nil && regexp.MustCompile(`(?i)Seccomp-BPF sandbox\s+(Yes|No)`).MatchString(last) && regexp.MustCompile(`(?i)(Namespace Sandbox\s+(Yes|No)|Layer 1 Sandbox\s+\S+)`).MatchString(last) {
			return StartFailure("sandbox_verification", "BROWSER_SANDBOX_REJECTED", fmt.Sprintf("Namespace sandbox confirmed=%t; seccomp-BPF sandbox confirmed=%t.", regexp.MustCompile(`(?i)(Namespace Sandbox\s+Yes|Layer 1 Sandbox\s+Namespace)`).MatchString(last), regexp.MustCompile(`(?i)Seccomp-BPF sandbox\s+Yes`).MatchString(last)))
		}
		select {
		case <-ctx.Done():
			return StartFailure("sandbox_verification", "BROWSER_START_TIMEOUT", "Sandbox report was not available before the initialization deadline.")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
func (b *Browser) Close() {
	b.once.Do(func() {
		b.cancel()
		if b.cdp != nil {
			b.cdp.close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, "systemctl", "--user", "stop", b.scope).Run()
		if b.cmd != nil && b.cmd.Process != nil {
			_ = b.cmd.Process.Kill()
		}
		select {
		case <-b.done:
		case <-ctx.Done():
		}
		b.Proxy.Close()
	})
}
func (b *Browser) Done() <-chan struct{} { return b.ctx.Done() }
func (b *Browser) TerminalCode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.terminalCode != "" {
		return b.terminalCode
	}
	return "BROWSER_CRASHED"
}
func (b *Browser) emit(e Event) bool {
	select {
	case b.Events <- e:
		return true
	default:
		if e.Kind == "newtab" {
			b.mu.Lock()
			b.terminalCode = "RESOURCE_LIMIT"
			b.mu.Unlock()
			b.cancel()
		}
		return false
	}
}
func (b *Browser) events() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-b.cdp.done:
			if b.cdp.eventOverflow.Load() {
				b.mu.Lock()
				b.terminalCode = "RESOURCE_LIMIT"
				b.mu.Unlock()
			}
			b.cancel()
			return
		case p := <-b.cdp.events:
			if p.Method == "Target.targetCreated" {
				var info struct {
					Info struct {
						ID   string `json:"targetId"`
						Type string `json:"type"`
					} `json:"targetInfo"`
				}
				if json.Unmarshal(p.Params, &info) == nil && info.Info.Type == "page" {
					b.mu.Lock()
					approved := b.approved[info.Info.ID]
					deferClose := b.creating > 0 && len(b.potential) < 32
					if !approved && deferClose {
						b.potential[info.Info.ID] = true
					}
					b.mu.Unlock()
					if !approved && !deferClose {
						ctx, cancel := context.WithTimeout(b.ctx, time.Second)
						_ = b.cdp.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": info.Info.ID}, nil)
						cancel()
					}
				}
				continue
			}

			b.mu.Lock()
			var tab string
			for id, page := range b.pages {
				if page.CDP == p.Session {
					tab = id
					break
				}
			}
			b.mu.Unlock()
			if tab == "" {
				continue
			}
			switch p.Method {
			case "Page.windowOpen":
				b.windowOpen(tab, p.Params)
			case "Page.frameNavigated":
				var data struct {
					Frame struct {
						ID     string `json:"id"`
						Parent string `json:"parentId"`
						URL    string `json:"url"`
					} `json:"frame"`
				}
				if json.Unmarshal(p.Params, &data) == nil && data.Frame.Parent == "" && len(data.Frame.URL) <= 8192 && browsernet.Validate(data.Frame.URL) == nil {
					b.mu.Lock()
					if page := b.pages[tab]; page != nil {
						page.MainFrame = data.Frame.ID
						page.LinkWorld = 0
						page.LinkFrame = ""
					}
					b.mu.Unlock()
					b.emit(Event{Tab: tab, State: "Loading", Target: data.Frame.URL})
				}
			case "Page.navigatedWithinDocument":
				var change struct {
					Frame string `json:"frameId"`
					URL   string `json:"url"`
				}
				if json.Unmarshal(p.Params, &change) == nil && browsernet.Validate(change.URL) == nil && len(change.URL) <= 8192 {
					b.mu.Lock()
					page := b.pages[tab]
					main := page != nil && page.MainFrame == change.Frame
					b.mu.Unlock()
					if main {
						b.emit(Event{Tab: tab, State: "Completed", Target: change.URL})
					}
				}
			case "Page.loadEventFired":
				b.emit(Event{Tab: tab, State: "Completed"})
			case "Network.responseReceived":
				var data struct {
					Type     string `json:"type"`
					Response struct {
						Status  int                        `json:"status"`
						Headers map[string]json.RawMessage `json:"headers"`
					} `json:"response"`
				}
				if json.Unmarshal(p.Params, &data) == nil && data.Type == "Document" {
					blocked := false
					failureCode := ""
					for key := range data.Response.Headers {
						if strings.EqualFold(key, "X-WolfTunnel-Policy-Rejected") {
							blocked = true
						}
						if strings.EqualFold(key, "X-WolfTunnel-Error") {
							var value string
							if json.Unmarshal(data.Response.Headers[key], &value) == nil && (value == "NODE_DNS_ERROR" || value == "NODE_UNREACHABLE" || value == "RESOURCE_LIMIT") {
								failureCode = value
							}
						}
					}
					if blocked {
						b.emit(Event{Tab: tab, State: "Failed", Code: "NODE_POLICY_REJECTED"})
						continue
					}
					if failureCode != "" {
						b.emit(Event{Tab: tab, State: "Failed", Code: failureCode})
						continue
					}
					b.emit(Event{Tab: tab, State: "Loading", Status: data.Response.Status})
				}
			case "Network.loadingFailed":
				var data struct {
					Type     string `json:"type"`
					Canceled bool   `json:"canceled"`
				}
				if json.Unmarshal(p.Params, &data) == nil && data.Type == "Document" {
					code := "NAVIGATION_FAILED"
					if data.Canceled {
						code = "CANCELLED"
					}
					b.emit(Event{Tab: tab, State: "Failed", Code: code})
				}
			case "Target.targetCrashed", "Inspector.targetCrashed":
				b.emit(Event{Tab: tab, State: "Failed", Code: "BROWSER_CRASHED"})
			}
		}
	}
}
func (b *Browser) NewPage(ctx context.Context, id string) error {
	b.mu.Lock()
	if _, ok := b.pages[id]; ok {
		b.mu.Unlock()
		return nil
	}
	if len(b.pages)+b.creating >= b.Config.MaxTabs {
		b.mu.Unlock()
		return errors.New("TAB_LIMIT")
	}
	b.creating++
	b.mu.Unlock()
	defer b.finishCreation(ctx)
	var target struct {
		ID string `json:"targetId"`
	}
	if e := b.cdp.call(ctx, "", "Target.createTarget", map[string]any{"url": "about:blank", "browserContextId": b.contextID}, &target); e != nil {
		return e
	}
	complete := false
	defer func() {
		if !complete {
			b.mu.Lock()
			delete(b.approved, target.ID)
			b.mu.Unlock()
			cleanup, stop := context.WithTimeout(b.ctx, time.Second)
			defer stop()
			b.cdp.call(cleanup, "", "Target.closeTarget", map[string]string{"targetId": target.ID}, nil)
		}
	}()
	b.mu.Lock()
	b.approved[target.ID] = true
	b.mu.Unlock()
	var attach struct {
		ID string `json:"sessionId"`
	}
	if e := b.cdp.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": target.ID, "flatten": true}, &attach); e != nil {
		return e
	}
	page := &Page{ID: id, Target: target.ID, CDP: attach.ID}

	for _, method := range []string{"Page.enable", "Network.enable"} {
		if e := b.cdp.call(ctx, page.CDP, method, map[string]any{}, nil); e != nil {
			return e
		}
	}
	if e := b.cdp.call(ctx, page.CDP, "Page.setInterceptFileChooserDialog", map[string]bool{"enabled": true}, nil); e != nil {
		return e
	}
	b.mu.Lock()
	b.pages[id] = page
	b.mu.Unlock()
	complete = true
	return nil
}
func (b *Browser) page(id string) (*Page, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pages[id]
	if p == nil {
		return nil, errors.New("SESSION_INVALID")
	}
	return p, nil
}
func (b *Browser) Select(ctx context.Context, id string, w, h int) error {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	p, e := b.page(id)
	if e != nil {
		return e
	}
	_ = b.clearInput(ctx)
	old, _, _ := b.Active()
	if old != "" && old != id {
		if previous, err := b.page(old); err == nil {
			_ = b.cdp.call(ctx, previous.CDP, "Page.setWebLifecycleState", map[string]string{"state": "frozen"}, nil)
		}
	}
	_ = b.cdp.call(ctx, p.CDP, "Page.setWebLifecycleState", map[string]string{"state": "active"}, nil)
	if e = b.cdp.call(ctx, "", "Target.activateTarget", map[string]string{"targetId": p.Target}, nil); e != nil {
		return e
	}
	if e = b.cdp.call(ctx, p.CDP, "Emulation.setDeviceMetricsOverride", map[string]any{"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false}, nil); e != nil {
		return e
	}
	b.mu.Lock()
	b.active = id
	b.width = w
	b.height = h
	b.mu.Unlock()
	return nil
}
func (b *Browser) Active() (string, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active, b.width, b.height
}
func (b *Browser) ClosePage(ctx context.Context, id string) error {
	p, e := b.page(id)
	if e != nil {
		return e
	}
	_ = b.clearInput(ctx)
	e = b.cdp.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": p.Target}, nil)
	b.mu.Lock()
	delete(b.pages, id)
	delete(b.approved, p.Target)
	if b.active == id {
		b.active = ""
	}
	b.mu.Unlock()
	return e
}
func (b *Browser) Navigate(ctx context.Context, id, target string) error {
	if browsernet.Validate(target) != nil {
		return browsernet.ErrPolicy
	}
	p, e := b.page(id)
	if e != nil {
		return e
	}
	_ = b.clearInput(ctx)
	var out struct {
		Error string `json:"errorText"`
	}
	if e = b.cdp.call(ctx, p.CDP, "Page.navigate", map[string]string{"url": target}, &out); e != nil {
		if ctx.Err() != nil {
			stop, cancel := context.WithTimeout(b.ctx, time.Second)
			_ = b.Cancel(stop, id)
			cancel()
		}
		return e
	}
	if out.Error != "" {
		return &navigationError{cause: out.Error}
	}
	return nil
}
func (b *Browser) Cancel(ctx context.Context, id string) error {
	p, e := b.page(id)
	if e != nil {
		return e
	}
	return b.cdp.call(ctx, p.CDP, "Page.stopLoading", map[string]any{}, nil)
}
func (b *Browser) History(ctx context.Context, id string, direction int) error {
	if direction != -1 && direction != 1 {
		return errors.New("PROTOCOL_ERROR")
	}
	p, e := b.page(id)
	if e != nil {
		return e
	}
	var out struct {
		Current int `json:"currentIndex"`
		Entries []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if e = b.cdp.call(ctx, p.CDP, "Page.getNavigationHistory", map[string]any{}, &out); e != nil {
		return e
	}
	idx := out.Current + direction
	if idx < 0 || idx >= len(out.Entries) {
		return nil
	}
	return b.cdp.call(ctx, p.CDP, "Page.navigateToHistoryEntry", map[string]int{"entryId": out.Entries[idx].ID}, nil)
}
func (b *Browser) CapturePage(ctx context.Context) ([]byte, int, int, string, error) {
	b.captureMu.RLock()
	defer b.captureMu.RUnlock()
	id, w, h := b.Active()
	if id == "" {
		return nil, 0, 0, "", nil
	}
	p, e := b.page(id)
	if e != nil {
		return nil, 0, 0, "", e
	}
	var out struct {
		Data string `json:"data"`
	}
	e = b.cdp.call(ctx, p.CDP, "Page.captureScreenshot", map[string]any{"format": "jpeg", "quality": b.Config.Quality, "fromSurface": true, "captureBeyondViewport": false}, &out)
	if e != nil {
		return nil, 0, 0, "", e
	}
	if len(out.Data) > 11<<20 {
		return nil, 0, 0, "", errors.New("PIXEL_ERROR")
	}
	raw, e := base64.StdEncoding.DecodeString(out.Data)
	return raw, w, h, id, e
}

func (b *Browser) Capture(ctx context.Context) ([]byte, int, int, error) {
	data, w, h, _, e := b.CapturePage(ctx)
	return data, w, h, e
}
func (b *Browser) Input(ctx context.Context, i v3.Input) error {
	id, w, h := b.Active()
	if i.Validate(w, h) != nil {
		return errors.New("PROTOCOL_ERROR")
	}
	p, e := b.page(id)
	if e != nil {
		return e
	}
	if b.navigationOn() {
		if i.Kind == "move" {
			b.queueHover(id, i)
		}
		if i.Kind == "up" && i.Button == 1 {
			b.mu.Lock()
			suppressed := b.interceptedClick
			b.interceptedClick = false
			b.mu.Unlock()
			if suppressed {
				return nil
			}
		}
		if i.Kind == "down" && i.Button == 1 && i.Modifiers&(2|4) != 0 {
			target, err := b.linkAt(ctx, p, i.X, i.Y)
			if err == nil && target != "" {
				b.mu.Lock()
				b.interceptedClick = true
				b.mu.Unlock()
				b.newTab(id, target, true)
				return nil
			}
		}
	}
	switch i.Kind {
	case "blur":
		return b.clearInput(ctx)
	case "text":
		return b.cdp.call(ctx, p.CDP, "Input.insertText", map[string]string{"text": i.Text}, nil)
	case "pinch":
		// Desktop headless Chromium acknowledges synthetic touch pinches without
		// zooming. Apply the native gesture's real relative scale through the
		// fixed page-scale API instead; no page script or touch emulation is used.
		var metrics struct {
			CSSVisualViewport struct {
				Scale float64 `json:"scale"`
			} `json:"cssVisualViewport"`
		}
		if err := b.cdp.call(ctx, p.CDP, "Page.getLayoutMetrics", nil, &metrics); err != nil {
			return err
		}
		scale := metrics.CSSVisualViewport.Scale
		if scale <= 0 {
			scale = 1
		}
		return b.cdp.call(ctx, p.CDP, "Emulation.setPageScaleFactor", map[string]any{"pageScaleFactor": max(1.0, min(5.0, scale*i.Scale))}, nil)
	case "key_down", "key_up":
		typ := "rawKeyDown"
		b.mu.Lock()
		if i.Kind == "key_up" {
			typ = "keyUp"
			delete(b.keys, i.Code)
		} else {
			b.keys[i.Code] = i
		}
		b.mu.Unlock()
		params := map[string]any{"type": typ, "code": i.Code, "key": i.Key, "modifiers": i.Modifiers, "windowsVirtualKeyCode": virtualKey(i.Code), "autoRepeat": i.Repeat}
		// SDL committed-text events exclude Enter. Chromium needs its character
		// semantics as well as the physical key to submit forms/newlines.
		if i.Kind == "key_down" && i.Code == "Enter" {
			params["type"], params["text"], params["unmodifiedText"] = "keyDown", "\r", "\r"
		}
		return b.cdp.call(ctx, p.CDP, "Input.dispatchKeyEvent", params, nil)
	default:
		typ := "mouseMoved"
		button := "none"
		if i.Button >= 1 && i.Button <= 3 {
			button = []string{"left", "middle", "right"}[i.Button-1]
		}
		b.mu.Lock()
		if i.Kind == "down" {
			typ = "mousePressed"
			b.buttons |= []int{1, 4, 2}[i.Button-1]
		} else if i.Kind == "up" {
			typ = "mouseReleased"
			b.buttons &^= []int{1, 4, 2}[i.Button-1]
		} else if i.Kind == "wheel" {
			typ = "mouseWheel"
		}
		buttons := b.buttons
		b.mu.Unlock()
		if i.Kind == "move" {
			if buttons&1 != 0 {
				button = "left"
			} else if buttons&4 != 0 {
				button = "middle"
			} else if buttons&2 != 0 {
				button = "right"
			}
		}
		params := map[string]any{"type": typ, "x": i.X, "y": i.Y, "button": button, "buttons": buttons, "modifiers": i.Modifiers}
		if i.Kind == "down" || i.Kind == "up" {
			params["clickCount"] = 1
		}
		if i.Kind == "wheel" {
			params["deltaX"] = i.DeltaX
			params["deltaY"] = i.DeltaY
		}
		return b.cdp.call(ctx, p.CDP, "Input.dispatchMouseEvent", params, nil)
	}
}
func (b *Browser) clearInput(ctx context.Context) error {
	b.mu.Lock()
	p := b.pages[b.active]
	b.interceptedClick = false
	b.hoverTarget = ""
	b.hoverEpoch++
	held := b.keys
	b.keys = make(map[string]v3.Input)
	buttons := b.buttons
	b.buttons = 0
	b.mu.Unlock()
	if p == nil {
		return nil
	}
	for _, i := range held {
		_ = b.cdp.call(ctx, p.CDP, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": i.Key, "code": i.Code, "windowsVirtualKeyCode": virtualKey(i.Code)}, nil)
	}
	for i := 1; i <= 3; i++ {
		if buttons&([]int{1, 4, 2}[i-1]) != 0 {
			_ = b.cdp.call(ctx, p.CDP, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": 0, "y": 0, "button": []string{"left", "middle", "right"}[i-1], "clickCount": 1}, nil)
		}
	}
	return nil
}
func virtualKey(code string) int {
	if strings.HasPrefix(code, "F") {
		if n, e := strconv.Atoi(strings.TrimPrefix(code, "F")); e == nil && n >= 1 && n <= 12 {
			return 111 + n
		}
	}
	if n, ok := map[string]int{"PageUp": 33, "PageDown": 34, "Insert": 45, "CapsLock": 20, "ShiftLeft": 16, "ShiftRight": 16, "ControlLeft": 17, "ControlRight": 17, "AltLeft": 18, "AltRight": 18, "MetaLeft": 91, "MetaRight": 92, "Minus": 189, "Equal": 187, "BracketLeft": 219, "BracketRight": 221, "Backslash": 220, "Semicolon": 186, "Quote": 222, "Comma": 188, "Period": 190, "Slash": 191, "Backquote": 192}[code]; ok {
		return n
	}
	if len(code) == 4 && strings.HasPrefix(code, "Key") {
		return int(code[3])
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		return int(code[5])
	}
	switch code {
	case "Enter":
		return 13
	case "Backspace":
		return 8
	case "Tab":
		return 9
	case "Escape":
		return 27
	case "Space":
		return 32
	case "ArrowLeft":
		return 37
	case "ArrowUp":
		return 38
	case "ArrowRight":
		return 39
	case "ArrowDown":
		return 40
	case "Delete":
		return 46
	case "Home":
		return 36
	case "End":
		return 35
	}
	return 0
}
func (b *Browser) String() string {
	return fmt.Sprintf("isolated Chromium (%d pages maximum)", b.Config.MaxTabs)
}

type startupBuffer struct {
	mu      sync.Mutex
	b       bytes.Buffer
	enabled bool
}

func (b *startupBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.enabled && b.b.Len() < 16384 {
		n := min(len(p), 16384-b.b.Len())
		_, _ = b.b.Write(p[:n])
	}
	return len(p), nil
}
func (b *startupBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }
func (b *startupBuffer) disable()       { b.mu.Lock(); b.enabled = false; b.b.Reset(); b.mu.Unlock() }

type navigationError struct{ cause string }

func (e *navigationError) Error() string { return "NAVIGATION_FAILED" }

// ActivePage checks page membership without exposing CDP identifiers.
func (b *Browser) ActivePage(id string) (string, error) {
	p, e := b.page(id)
	if e != nil {
		return "", e
	}
	return p.ID, nil
}

// Resources reports only resource counters from this session's transient scope.
type ResourceStats struct{ Memory, MemoryPeak, CPUTimeNS, Tasks uint64 }

func (b *Browser) Resources(ctx context.Context) (ResourceStats, error) {
	out, e := exec.CommandContext(ctx, "systemctl", "--user", "show", b.scope, "-p", "MemoryCurrent", "-p", "MemoryPeak", "-p", "CPUUsageNSec", "-p", "TasksCurrent").Output()
	if e != nil {
		return ResourceStats{}, e
	}
	var stats ResourceStats
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		n, _ := strconv.ParseUint(value, 10, 64)
		switch key {
		case "MemoryCurrent":
			stats.Memory = n
		case "MemoryPeak":
			stats.MemoryPeak = n
		case "CPUUsageNSec":
			stats.CPUTimeNS = n
		case "TasksCurrent":
			stats.Tasks = n
		}
	}
	return stats, nil
}

func (b *Browser) finishCreation(ctx context.Context) {
	b.mu.Lock()
	b.creating--
	pending := b.potential
	b.potential = make(map[string]bool)
	allowed := make(map[string]bool, len(b.approved))
	for id, v := range b.approved {
		allowed[id] = v
	}
	b.mu.Unlock()
	for target := range pending {
		if !allowed[target] {
			_ = b.cdp.call(ctx, "", "Target.closeTarget", map[string]string{"targetId": target}, nil)
		}
	}
}
