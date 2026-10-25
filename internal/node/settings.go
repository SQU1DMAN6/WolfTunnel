package node

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"wolftunnel/internal/storage"
)

// Settings are saved operator conveniences, separate from security/resource
// policy in config.json. A zero-flag launch reuses them.
type Settings struct {
	Listen          string `json:"listen"`
	PublicAddress   string `json:"public_address,omitempty"`
	BrowserConfig   string `json:"browser_config,omitempty"`
	ManagedRenderer bool   `json:"managed_renderer"`
	RendererDir     string `json:"renderer_dir,omitempty"`
}

func DefaultSettings() Settings { return Settings{Listen: ":13300", ManagedRenderer: true} }
func LoadSettings(root string) (Settings, bool, error) {
	s := DefaultSettings()
	if e := storage.Dir(root); e != nil {
		return s, false, e
	}
	e := storage.Read(filepath.Join(root, "settings.json"), &s)
	if errors.Is(e, os.ErrNotExist) {
		return s, true, nil
	}
	if e != nil {
		return s, false, e
	}
	return s, false, s.Valid()
}
func (s Settings) Valid() error {
	for _, address := range []string{s.Listen, s.PublicAddress} {
		if address == "" {
			continue
		}
		_, p, e := net.SplitHostPort(address)
		if e != nil {
			return errors.New("INVALID_NODE_SETTINGS")
		}
		port, e := strconv.Atoi(p)
		if e != nil || port < 0 || port > 65535 {
			return errors.New("INVALID_NODE_SETTINGS")
		}
	}
	if s.Listen == "" {
		return errors.New("INVALID_NODE_SETTINGS")
	}
	return nil
}
func (s Settings) Save(root string) error {
	if e := s.Valid(); e != nil {
		return e
	}
	return storage.Write(filepath.Join(root, "settings.json"), s)
}
func (s Settings) InvitationAddress() string {
	if s.PublicAddress != "" {
		return s.PublicAddress
	}
	host, port, _ := net.SplitHostPort(s.Listen)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
