package audit

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Logger struct {
	mu        sync.Mutex
	dir       string
	retention time.Duration
}

type Entry struct {
	Time   string `json:"time"`
	Event  string `json:"event"`
	Target string `json:"target,omitempty"`
	ID     string `json:"request_id,omitempty"`
	Status string `json:"status,omitempty"`
}

func New(dir string, retention time.Duration) (*Logger, error) {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0700)
	l := &Logger{dir: dir, retention: retention}
	l.Purge()
	return l, nil
}

// SanitizeURL intentionally strips userinfo, query parameters and fragments.
// The path remains useful for audit purposes but may itself contain sensitive values.
func SanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "[invalid-url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

func (l *Logger) Log(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	name := "wolfnode-" + time.Now().UTC().Format("2006-01-02") + ".jsonl"
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_ = json.NewEncoder(f).Encode(e)
	_ = f.Close()
	l.purgeLocked()
}

func (l *Logger) Purge() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.purgeLocked()
}

func (l *Logger) purgeLocked() {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-l.retention)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "wolfnode-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(l.dir, e.Name())
		info, err := os.Stat(p)
		if err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
		}
	}
}
