//go:build linux

package browser

import (
	"io"
	"net"
	"os"
	"os/exec"
	"syscall"
)

// This private subprocess mode is entered only by the isolated launcher. CDP
// pipes remain private FDs 3/4. The namespace has no external network devices;
// this loopback forwarder can reach only the parent's validating Unix proxy.
func init() {
	if len(os.Args) == 3 && os.Args[1] == "__wolf_browser_worker" {
		os.Exit(worker(os.Args[2]))
	}
}
func worker(binary string) int {
	if syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{Cur: 0, Max: 0}) != nil {
		return 73
	}
	ln, e := net.Listen("tcp", "127.0.0.1:18080")
	if e != nil {
		return 71
	}
	defer ln.Close()
	slots := make(chan struct{}, 64)
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				c.Close()
				continue
			}
			go func() {
				defer func() { <-slots }()
				defer c.Close()
				up, e := net.Dial("unix", "/egress/proxy.sock")
				if e != nil {
					return
				}
				defer up.Close()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(up, c); up.Close(); close(done) }()
				_, _ = io.Copy(c, up)
				c.Close()
				up.Close()
				<-done
			}()
		}
	}()
	args := []string{"--headless=new", "--remote-debugging-pipe", "--user-data-dir=/tmp/profile", "--proxy-server=http://127.0.0.1:18080", "--proxy-bypass-list=<-loopback>", "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1", "--disable-quic", "--dns-prefetch-disable", "--disable-extensions", "--disable-sync", "--disable-component-update", "--no-first-run", "--no-default-browser-check", "--disable-default-apps", "--disable-background-networking", "--disable-client-side-phishing-detection", "--disable-breakpad", "--disable-crash-reporter", "--disable-domain-reliability", "--disable-features=MediaRouter,OptimizationHints,AutofillServerCommunication,Translate,Prerender2", "--mute-audio", "--disable-speech-api", "--disable-notifications", "--disable-gpu", "--num-raster-threads=2", "--renderer-process-limit=8", "--force-webrtc-ip-handling-policy=disable_non_proxied_udp", "--disk-cache-size=16777216", "--media-cache-size=1", "--password-store=basic", "about:blank"}
	cmd := exec.Command(binary, args...)
	cmd.Env = []string{"HOME=/home/wolf", "PATH=/usr/bin", "LANG=C.UTF-8", "XDG_CONFIG_HOME=/tmp/config", "XDG_CACHE_HOME=/tmp/cache"}
	cmd.ExtraFiles = []*os.File{os.NewFile(3, "cdp-in"), os.NewFile(4, "cdp-out")}
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if e = cmd.Run(); e != nil {
		return 72
	}
	return 0
}
