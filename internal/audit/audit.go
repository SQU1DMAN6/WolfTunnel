package audit

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wolftunnel/internal/storage"
)

type Logger struct {
	mu        sync.Mutex
	dir       string
	retention time.Duration
	failed    bool
	prefix    string
}

type Entry struct {
	Time       string `json:"time"`
	Event      string `json:"event"`
	Target     string `json:"target,omitempty"`
	ID         string `json:"request_id,omitempty"`
	Status     string `json:"status,omitempty"`
	ClientID   string `json:"client_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Code       string `json:"code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Component  string `json:"component,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Frames     uint64 `json:"frames,omitempty"`
	PixelBytes uint64 `json:"pixel_bytes,omitempty"`
	Dropped    uint64 `json:"dropped_frames,omitempty"`
}

func New(dir string, retention time.Duration) (*Logger, error) {
	return newLogger(dir, retention, "wolfnode-")
}
func NewClient(dir string, retention time.Duration) (*Logger, error) {
	return newLogger(dir, retention, "wolfclient-")
}
func newLogger(dir string, retention time.Duration, prefix string) (*Logger, error) {
	if retention <= 0 {
		retention = 7 * 24 * time.Hour
	}
	if err := storage.Dir(dir); err != nil {
		return nil, err
	}
	l := &Logger{dir: dir, retention: retention, prefix: prefix}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			info, err := os.Lstat(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return nil, fmt.Errorf("audit files must be private regular files")
			}
		}
	}
	l.Purge()
	return l, nil
}

// HostOnly excludes paths as well as credentials, queries and fragments.
func HostOnly(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "[invalid-url]"
	}
	return u.Scheme + "://" + u.Host
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
	if err := l.writeLocked(e); err != nil && !l.failed {
		l.failed = true
		log.Print("WolfTunnel: diagnostic/audit write failed; inspect state storage")
	}
}

func (l *Logger) writeLocked(e Entry) error {
	if e.Target != "" {
		e.Target = HostOnly(e.Target)
	}
	e.Time = time.Now().UTC().Format(time.RFC3339Nano)
	name := l.prefix + time.Now().UTC().Format("2006-01-02T15") + ".jsonl"
	path := filepath.Join(l.dir, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("insecure audit file")
		}
		if info.Size() >= 1<<20 {
			if err = os.Rename(path, filepath.Join(l.dir, l.prefix+time.Now().UTC().Format("2006-01-02T15")+"-"+fmt.Sprint(time.Now().UnixNano())+".jsonl")); err != nil {
				return err
			}
		}
	}
	f, err := os.OpenFile(filepath.Join(l.dir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	l.purgeLocked()
	return nil
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
	remaining := len(entries)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), l.prefix) || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(l.dir, e.Name())
		info, err := os.Stat(p)
		expired := false
		start := len(l.prefix)
		if len(e.Name()) >= start+13 {
			if created, e := time.Parse("2006-01-02T15", e.Name()[start:start+13]); e == nil {
				expired = created.Before(cutoff)
			}
		}
		if err == nil && (expired || info.ModTime().Before(cutoff) || remaining > 64) {
			_ = os.Remove(p)
			remaining--
		}
	}
}
