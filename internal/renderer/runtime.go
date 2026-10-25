// Package renderer provisions WolfTunnel's pinned, application-private renderer.
// It never discovers or uses browsers installed by the operating system.
package renderer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"wolftunnel/internal/storage"
)

//go:embed runtime-lock.json
var manifestJSON []byte

type Manifest struct {
	Version       string            `json:"version"`
	Platform      string            `json:"platform"`
	URL           string            `json:"url"`
	ArchiveSHA256 string            `json:"archive_sha256"`
	Binary        string            `json:"binary"`
	BinarySHA256  string            `json:"binary_sha256"`
	Files         map[string]string `json:"files"`
	Source        string            `json:"source"`
}

func Lock() Manifest {
	var m Manifest
	if json.Unmarshal(manifestJSON, &m) != nil {
		panic("invalid renderer build manifest")
	}
	return m
}

type Installation struct{ Binary, SHA256, Version string }

var ErrIntegrity = errors.New("RENDERER_INTEGRITY_FAILED")

const maxArchive = 256 << 20
const maxUnpacked = 768 << 20

func Directory(base string) string { m := Lock(); return filepath.Join(base, m.Version+"-"+m.Platform) }
func result(dir string, m Manifest) Installation {
	return Installation{filepath.Join(dir, filepath.FromSlash(m.Binary)), m.BinarySHA256, m.Version}
}

// Ensure verifies an existing package or installs it atomically. A corrupted
// existing package fails closed instead of silently trusting a different engine.
// archivePath allows offline provisioning from the same pinned archive.
func Ensure(ctx context.Context, base, archivePath string) (Installation, error) {
	if ctx.Err() != nil {
		return Installation{}, ctx.Err()
	}
	m := Lock()
	if runtime.GOOS+"-"+runtime.GOARCH != m.Platform {
		return Installation{}, errors.New("RENDERER_PLATFORM_UNSUPPORTED")
	}
	if e := storage.Dir(base); e != nil {
		return Installation{}, e
	}
	f, e := os.OpenFile(filepath.Join(base, ".install.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return Installation{}, e
	}
	defer f.Close()
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			break
		}
		if e != syscall.EWOULDBLOCK {
			return Installation{}, e
		}
		select {
		case <-ctx.Done():
			return Installation{}, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	dir := Directory(base)
	if _, e = os.Lstat(dir); e == nil {
		if e = verify(ctx, dir, m); e != nil {
			return Installation{}, e
		}
		return result(dir, m), nil
	} else if !os.IsNotExist(e) {
		return Installation{}, e
	}
	archive, e := os.CreateTemp(base, ".runtime-*.zip")
	if e != nil {
		return Installation{}, e
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	if archivePath != "" {
		sourceInfo, e := os.Lstat(archivePath)
		if e != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Size() > maxArchive {
			return Installation{}, ErrIntegrity
		}
		source, e := os.Open(archivePath)
		if e != nil {
			return Installation{}, e
		}
		_, e = io.Copy(archive, io.LimitReader(source, maxArchive+1))
		source.Close()
		if e != nil {
			return Installation{}, e
		}
	} else {
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, m.URL, nil)
		if e != nil {
			return Installation{}, e
		}
		// This is an installer download, separate from isolated webpage egress. Never
		// send browser/user data and never follow redirects to arbitrary hosts/HTTP.
		client := &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 3 || r.URL.Scheme != "https" || r.URL.Host != "storage.googleapis.com" {
				return ErrIntegrity
			}
			return nil
		}}
		response, e := client.Do(request)
		if e != nil {
			return Installation{}, errors.New("RENDERER_DOWNLOAD_FAILED")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return Installation{}, fmt.Errorf("RENDERER_DOWNLOAD_FAILED: HTTP %d", response.StatusCode)
		}
		_, e = io.Copy(archive, io.LimitReader(response.Body, maxArchive+1))
		response.Body.Close()
		if e != nil {
			return Installation{}, errors.New("RENDERER_DOWNLOAD_FAILED")
		}
	}
	info, e := archive.Stat()
	if e != nil || info.Size() > maxArchive {
		return Installation{}, ErrIntegrity
	}
	if _, e = archive.Seek(0, io.SeekStart); e != nil {
		return Installation{}, e
	}
	hash := sha256.New()
	if _, e = io.Copy(hash, archive); e != nil {
		return Installation{}, e
	}
	if hex.EncodeToString(hash.Sum(nil)) != m.ArchiveSHA256 {
		return Installation{}, ErrIntegrity
	}
	temporary, e := os.MkdirTemp(base, ".unpack-")
	if e != nil {
		return Installation{}, e
	}
	defer os.RemoveAll(temporary)
	if e = unpack(ctx, archive.Name(), temporary, m); e != nil {
		return Installation{}, e
	}
	if e = verify(ctx, temporary, m); e != nil {
		return Installation{}, e
	}
	if e = os.Rename(temporary, dir); e != nil {
		return Installation{}, e
	}
	return result(dir, m), nil
}
func safePath(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.ContainsRune(name, 0) && !filepath.IsAbs(name) && filepath.ToSlash(filepath.Clean(name)) == name && name != ".." && !strings.HasPrefix(name, "../")
}
func unpack(ctx context.Context, archive, dir string, m Manifest) error {
	z, e := zip.OpenReader(archive)
	if e != nil {
		return ErrIntegrity
	}
	defer z.Close()
	if len(z.File) > 1024 {
		return ErrIntegrity
	}
	seen := map[string]bool{}
	var total uint64
	for _, entry := range z.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !safePath(entry.Name) || m.Files[entry.Name] == "" || seen[entry.Name] || !entry.Mode().IsRegular() {
			return ErrIntegrity
		}
		total += entry.UncompressedSize64
		if total > maxUnpacked || entry.UncompressedSize64 > maxUnpacked {
			return ErrIntegrity
		}
		seen[entry.Name] = true
		target := filepath.Join(dir, filepath.FromSlash(entry.Name))
		if e = os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if entry.Mode()&0111 != 0 {
			mode = 0755
		}
		output, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		input, e := entry.Open()
		if e != nil {
			output.Close()
			return ErrIntegrity
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil || n != int64(entry.UncompressedSize64) {
			return ErrIntegrity
		}
	}
	if len(seen) != len(m.Files) {
		return ErrIntegrity
	}
	return nil
}
func verify(ctx context.Context, dir string, m Manifest) error {
	info, e := os.Lstat(dir)
	if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return ErrIntegrity
	}
	count := 0
	e = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrIntegrity
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		info, e := entry.Info()
		if e != nil || info.Mode().Perm()&0022 != 0 || entry.Type()&os.ModeSymlink != 0 {
			return ErrIntegrity
		}
		if entry.IsDir() {
			return nil
		}
		name, e := filepath.Rel(dir, path)
		if e != nil || !info.Mode().IsRegular() {
			return ErrIntegrity
		}
		if filepath.ToSlash(name) == m.Binary && info.Mode().Perm()&0111 == 0 {
			return ErrIntegrity
		}
		expected := m.Files[filepath.ToSlash(name)]
		if expected == "" {
			return ErrIntegrity
		}
		f, e := os.Open(path)
		if e != nil {
			return ErrIntegrity
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil || hex.EncodeToString(h.Sum(nil)) != expected {
			return ErrIntegrity
		}
		count++
		return nil
	})
	if e != nil {
		return e
	}
	if count != len(m.Files) {
		return ErrIntegrity
	}
	return nil
}

// Bundled finds an optional verified runtime next to a release executable.
func Bundled(ctx context.Context) (Installation, bool, error) {
	exe, e := os.Executable()
	if e != nil {
		return Installation{}, false, e
	}
	base := filepath.Join(filepath.Dir(exe), "renderer")
	dir := Directory(base)
	if _, e = os.Lstat(dir); os.IsNotExist(e) {
		return Installation{}, false, nil
	} else if e != nil {
		return Installation{}, true, e
	}
	m := Lock()
	if e = verify(ctx, dir, m); e != nil {
		return Installation{}, true, e
	}
	return result(dir, m), true, nil
}
