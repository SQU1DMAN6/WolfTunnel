package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"wolftunnel/internal/clientui"
	"wolftunnel/internal/nodeclient"
)

type appConfig struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Fingerprint  string `json:"fingerprint"`
	Token        string `json:"token"`
	SearchEngine string `json:"search_engine"`
}
type appStatus struct {
	Connected bool   `json:"connected"`
	Message   string `json:"message"`
	Last      string `json:"last"`
}
type app struct {
	mu     sync.RWMutex
	cfg    appConfig
	status appStatus
}
type requestBody struct {
	Input  string `json:"input"`
	Target string `json:"target"`
	Method string `json:"method"`
}

func main() {
	listenFlag := flag.String("listen", "127.0.0.1:8787", "local UI listen address (keep loopback-only)")
	flag.Parse()
	listen := *listenFlag
	a := &app{cfg: appConfig{Host: "129.150.63.22", Port: 13300, SearchEngine: "duckduckgo"}, status: appStatus{Message: "Node not tested", Last: "No request sent"}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.home)
	mux.HandleFunc("/api/config", a.configHandler)
	mux.HandleFunc("/api/status", a.statusHandler)
	mux.HandleFunc("/api/ping", a.pingHandler)
	mux.HandleFunc("/api/request", a.requestHandler)
	server := &http.Server{Addr: listen, Handler: securityHeaders(mux), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatalf("local UI listen failed: %v", err)
	}
	fmt.Printf("WolfTunnel client UI: http://%s\n", listen)
	fmt.Println("Configuration and browsing state are memory-only. Close this process to end the local session.")
	go func() { time.Sleep(350 * time.Millisecond); openBrowser("http://" + listen) }()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *app) home(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	_, _ = w.Write(clientui.HTML)
}

func (a *app) configHandler(w http.ResponseWriter, r *http.Request) {
	if !a.localRequest(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.mu.RLock()
		c := a.cfg
		c.Token = ""
		a.mu.RUnlock()
		writeJSON(w, map[string]any{"ok": true, "config": c})
	case http.MethodPost:
		var in appConfig
		if err := decodeJSON(w, r, &in); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if strings.TrimSpace(in.Host) == "" || strings.ContainsAny(in.Host, "/ \t\r\n") {
			writeJSON(w, map[string]any{"ok": false, "error": "invalid Wolf Node host"})
			return
		}
		if in.Port < 1 || in.Port > 65535 {
			writeJSON(w, map[string]any{"ok": false, "error": "port must be between 1 and 65535"})
			return
		}
		if in.SearchEngine != "google" && in.SearchEngine != "duckduckgo" {
			in.SearchEngine = "duckduckgo"
		}
		a.mu.Lock()
		a.cfg = in
		a.status = appStatus{Message: "Configuration updated; connection not tested", Last: a.status.Last}
		a.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *app) statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	writeJSON(w, map[string]any{"ok": true, "status": a.status, "endpoint": fmt.Sprintf("%s:%d", a.cfg.Host, a.cfg.Port)})
}

func (a *app) pingHandler(w http.ResponseWriter, r *http.Request) {
	if !a.localRequest(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	c := a.getConfig()
	result, err := nodeclient.Ping(toNodeConfig(c))
	if err != nil {
		a.setStatus(false, "Connection failed: "+safeError(err))
		writeJSON(w, map[string]any{"ok": false, "error": safeError(err)})
		return
	}
	a.setStatus(true, result.State)
	writeJSON(w, map[string]any{"ok": true, "state": result.State})
}

func (a *app) requestHandler(w http.ResponseWriter, r *http.Request) {
	if !a.localRequest(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var in requestBody
	if err := decodeJSON(w, r, &in); err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	target, err := normaliseTarget(in.Input, in.Target, in.Method)
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	id := randomID()
	c := a.getConfig()
	result, err := nodeclient.SendRequest(toNodeConfig(c), id, in.Method, target)
	if err != nil {
		msg := safeError(err)
		a.setStatus(false, "Request failed: "+msg)
		writeJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	a.setStatus(true, "Connected · authenticated")
	writeJSON(w, map[string]any{"ok": true, "request_id": result.RequestID, "status": result.Status, "state": result.State, "can_respond": result.CanRespond})
}

func normaliseTarget(input, provided, engine string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", fmt.Errorf("enter a URL or search terms")
	}
	candidate := strings.TrimSpace(provided)
	if candidate == "" {
		candidate = s
	}
	if !strings.Contains(candidate, "://") && !strings.ContainsAny(candidate, " \t\r\n") && strings.Contains(candidate, ".") {
		candidate = "https://" + candidate
	}
	u, err := url.Parse(candidate)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil {
		return u.String(), nil
	}
	q := url.QueryEscape(s)
	if engine == "google" {
		return "https://www.google.com/search?q=" + q, nil
	}
	return "https://duckduckgo.com/?q=" + q, nil
}

func (a *app) localRequest(w http.ResponseWriter, r *http.Request) bool {
	// Restrict the UI API to loopback and same-origin browser requests.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		http.Error(w, "loopback clients only", http.StatusForbidden)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || (u.Host != r.Host) || (u.Scheme != "http") {
			http.Error(w, "origin rejected", http.StatusForbidden)
			return false
		}
	}
	return true
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return fmt.Errorf("empty request body")
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid request JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
func (a *app) getConfig() appConfig { a.mu.RLock(); defer a.mu.RUnlock(); return a.cfg }
func (a *app) setStatus(ok bool, msg string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = appStatus{Connected: ok, Message: msg, Last: msg}
}
func toNodeConfig(c appConfig) nodeclient.Config {
	return nodeclient.Config{Host: c.Host, Port: c.Port, Fingerprint: c.Fingerprint, Token: c.Token, SearchEngine: c.SearchEngine}
}
func randomID() string {
	var b [12]byte
	if _, e := rand.Read(b[:]); e != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
func safeError(err error) string {
	s := err.Error()
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 220 {
		s = s[:220]
	}
	return s
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store, max-age=0")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return
	}
	_ = cmd.Start()
}
