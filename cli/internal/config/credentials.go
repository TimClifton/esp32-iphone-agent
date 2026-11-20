package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

const pairingKeySize = 32

// Read returns nil without an error if a credential does not exist. Delete is idempotent.
type keyStore interface {
	Read(id string) ([]byte, error)
	Write(id string, key []byte) error
	Delete(id string) error
}

func (c *Config) loadKeys() (changed bool, err error) {
	c.keys = make(map[string][]byte, len(c.Boards))
	for id, board := range c.Boards {
		if !validBoardID(id) {
			return false, errors.New("invalid board ID in pairing configuration")
		}
		stored, err := c.secrets.Read(id)
		if err != nil {
			return false, fmt.Errorf("read pairing credential: %w", err)
		}
		if board.Key != "" {
			legacy, err := hex.DecodeString(board.Key)
			if err != nil || len(legacy) != pairingKeySize {
				return false, errors.New("invalid legacy pairing key in configuration")
			}
			switch {
			case stored == nil:
				if err := c.writeVerifiedKey(id, legacy); err != nil {
					return false, fmt.Errorf("migrate pairing credential: %w", err)
				}
				stored = legacy
			case !bytes.Equal(stored, legacy):
				return false, errors.New("legacy pairing key conflicts with the protected credential")
			}
			board.Key = ""
			c.Boards[id] = board
			changed = true
		}
		if len(stored) != pairingKeySize {
			return false, errors.New("protected pairing credential missing or invalid for a configured board")
		}
		c.keys[id] = stored
	}
	return changed, nil
}

func (c *Config) writeVerifiedKey(id string, key []byte) error {
	if err := c.secrets.Write(id, key); err != nil {
		return fmt.Errorf("write pairing credential: %w", err)
	}
	return c.verifyKey(id, key)
}

func (c *Config) verifyKey(id string, key []byte) error {
	readback, err := c.secrets.Read(id)
	if err != nil {
		return fmt.Errorf("verify pairing credential: %w", err)
	}
	if !bytes.Equal(readback, key) {
		return errors.New("pairing credential verification failed")
	}
	return nil
}

func (c *Config) setProtectedKey(id string, key []byte, name string) error {
	if !validBoardID(id) {
		return errors.New("invalid pairing board ID")
	}
	if len(key) != pairingKeySize {
		return errors.New("invalid pairing key length")
	}
	previous, err := c.secrets.Read(id)
	if err != nil {
		return fmt.Errorf("read previous pairing credential: %w", err)
	}
	if err := c.secrets.Write(id, key); err != nil {
		return fmt.Errorf("write pairing credential: %w", err)
	}
	if err := c.verifyKey(id, key); err != nil {
		return c.restorePairingKey(id, previous, err)
	}
	if err := c.saveBoard(id, Board{Name: name}); err != nil {
		return c.restorePairingKey(id, previous, err)
	}
	c.keys[id] = bytes.Clone(key)
	return nil
}

func validBoardID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 6 && hex.EncodeToString(decoded) == id
}

func (c *Config) restorePairingKey(id string, previous []byte, changeError error) error {
	if previous == nil {
		if err := c.secrets.Delete(id); err != nil {
			return errors.Join(changeError, fmt.Errorf("remove new pairing credential after failed pairing: %w", err))
		}
	} else if err := c.writeVerifiedKey(id, previous); err != nil {
		return errors.Join(changeError, fmt.Errorf("restore previous pairing credential: %w", err))
	}
	return changeError
}

// saveBoard updates metadata and restores the in-memory state if the file write fails.
func (c *Config) saveBoard(id string, board Board) error {
	previous, exists := c.Boards[id]
	c.Boards[id] = board
	if err := c.saveLocked(); err != nil {
		if exists {
			c.Boards[id] = previous
		} else {
			delete(c.Boards, id)
		}
		return err
	}
	return nil
}
