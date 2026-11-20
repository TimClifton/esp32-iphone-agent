// Package config keeps the CLI's identity and paired board configuration.
package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Dir is os.UserConfigDir()/esp32agent, or $ESP32AGENT_HOME.
func Dir() (string, error) {
	if d := os.Getenv("ESP32AGENT_HOME"); d != "" {
		return d, os.MkdirAll(d, 0o700)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "esp32agent")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, os.Chmod(d, 0o700)
}

// Board is one paired board.
type Board struct {
	Key  string `json:"key,omitempty"` // Legacy/non-Windows K, hex
	Name string `json:"name,omitempty"`
}

// Listen is `esp32agent listen`: hearing what the phone plays while it mirrors (docs/cli-protocol.md, "Phone audio").
type Listen struct {
	On     bool   `json:"on"`
	Mode   string `json:"mode,omitempty"`   // "phone" (the app's words, default) or "local" (whisper.cpp here)
	Second string `json:"second,omitempty"` // local: the language besides English ("zh"); "" for this computer's
}

// ListenMode is Mode with its default.
func (l Listen) ListenMode() string {
	if l.Mode == "local" {
		return "local"
	}
	return "phone"
}

// GetListen and SetListen guard Listen while the daemon uses it.
func (c *Config) GetListen() Listen {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Listen
}

func (c *Config) SetListen(l Listen) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous := c.Listen
	c.Listen = l
	if err := c.saveLocked(); err != nil {
		c.Listen = previous
		return err
	}
	return nil
}

// Config is config.json. Windows pairing keys are loaded separately into memory.
type Config struct {
	ClientID string           `json:"client_id"`
	Boards   map[string]Board `json:"boards"` // board id (12 hex) → metadata
	Listen   Listen           `json:"listen"`

	path        string
	mu          sync.Mutex
	secrets     keyStore
	keys        map[string][]byte
	writeConfig func(string, []byte) error
}

// Load reads config.json, creating the client id on first use.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	secrets, err := platformKeyStore(dir)
	if err != nil {
		return nil, fmt.Errorf("open pairing credential store: %w", err)
	}
	return load(filepath.Join(dir, "config.json"), secrets, writeConfigFile)
}

func load(path string, secrets keyStore, writeConfig func(string, []byte) error) (*Config, error) {
	c := &Config{path: path, Boards: map[string]Board{}, secrets: secrets, writeConfig: writeConfig}
	raw, err := os.ReadFile(c.path)
	if err == nil {
		if err := json.Unmarshal(raw, c); err != nil {
			return nil, err
		}
		if c.Boards == nil {
			c.Boards = map[string]Board{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var changed bool
	if c.secrets != nil {
		changed, err = c.loadKeys()
		if err != nil {
			return nil, err
		}
	}
	if len(c.ClientIDBytes()) != 16 {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		c.ClientID = hex.EncodeToString(id)
		changed = true
	}
	if changed {
		if err := c.saveLocked(); err != nil {
			return nil, fmt.Errorf("save pairing configuration: %w", err)
		}
	}
	return c, nil
}

func (c *Config) ClientIDBytes() []byte {
	b, _ := hex.DecodeString(c.ClientID)
	return b
}

// Save writes the file atomically with mode 0600.
func (c *Config) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

func (c *Config) saveLocked() error {
	raw, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	return c.writeConfig(c.path, raw)
}

func writeConfigFile(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".esp32agent-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceConfigFile(tmp.Name(), path)
}

// Key returns K for a board id, nil if not paired.
func (c *Config) Key(boardID []byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := hex.EncodeToString(boardID)
	if c.secrets != nil {
		return bytes.Clone(c.keys[id])
	}
	k, _ := hex.DecodeString(c.Boards[id].Key)
	if len(k) != pairingKeySize {
		return nil
	}
	return k
}

func (c *Config) SetKey(boardID, k []byte, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := hex.EncodeToString(boardID)
	if c.secrets != nil {
		return c.setProtectedKey(id, k, name)
	}
	return c.saveBoard(id, Board{Key: hex.EncodeToString(k), Name: name})
}

func (c *Config) Forget(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	previous, exists := c.Boards[id]
	delete(c.Boards, id)
	if err := c.saveLocked(); err != nil {
		if exists {
			c.Boards[id] = previous
		}
		return err
	}
	if c.secrets != nil {
		if err := c.secrets.Delete(id); err != nil {
			if exists {
				c.Boards[id] = previous
				return errors.Join(fmt.Errorf("remove pairing credential: %w", err), c.saveLocked())
			}
			return fmt.Errorf("remove pairing credential: %w", err)
		}
		delete(c.keys, id)
	}
	os.Remove(CalibrationPath(filepath.Dir(c.path), id))
	return nil
}

// Match finds paired board ids for a selector: a full id, or the 4-hex name suffix ("ESP32 Agent 6AD5").
func (c *Config) Match(sel string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	sel = strings.ToLower(sel)
	var out []string
	for id := range c.Boards {
		if id == sel || len(sel) == 4 && strings.HasSuffix(id, sel) {
			out = append(out, id)
		}
	}
	return out
}

// CalibrationPath is where a board's pointer calibration lives.
func CalibrationPath(dir, boardID string) string {
	return filepath.Join(dir, "calibration-"+boardID+".json")
}
