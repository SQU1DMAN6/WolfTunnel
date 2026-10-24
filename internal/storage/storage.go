// Package storage provides fail-closed private files and atomic replacements.
package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func DefaultDir(name string) (string, error) {
	h, e := os.UserHomeDir()
	return filepath.Join(h, name), e
}

func Dir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory must be private (0700): %s", path)
	}
	return nil
}

func Read(path string, v any) error {
	i, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 || i.Size() > 1<<20 {
		return fmt.Errorf("invalid private file permissions or size: %s", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return errors.New("null state file")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("invalid state file: %s", path)
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing data in state file")
	}
	return nil
}

func Write(path string, v any) error {
	return replace(path, v, false)
}

// Create atomically publishes new state without replacing an existing file.
func Create(path string, v any) error { return replace(path, v, true) }

func replace(path string, v any, exclusive bool) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if exclusive {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
