package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testID = "010203040506"

var fakeFailure = errors.New("simulated storage failure")

type fakeKeyStore struct {
	keys        map[string][]byte
	failWrite   bool
	failRead    bool
	failDelete  bool
	badReadback bool
	writes      int
}

func (s *fakeKeyStore) Read(id string) ([]byte, error) {
	if s.failRead {
		return nil, fakeFailure
	}
	if s.badReadback && s.writes > 0 {
		return bytes.Repeat([]byte{8}, pairingKeySize), nil
	}
	return bytes.Clone(s.keys[id]), nil
}

func (s *fakeKeyStore) Write(id string, key []byte) error {
	if s.failWrite {
		return fakeFailure
	}
	s.keys[id] = bytes.Clone(key)
	s.writes++
	return nil
}

func (s *fakeKeyStore) Delete(id string) error {
	if s.failDelete {
		return fakeFailure
	}
	delete(s.keys, id)
	return nil
}

func testProfile(t *testing.T) (path string, key []byte, legacy []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.json")
	key = bytes.Repeat([]byte{3}, pairingKeySize)
	legacy = []byte(`{"client_id":"00112233445566778899aabbccddeeff","boards":{"` + testID +
		`":{"key":"` + hex.EncodeToString(key) + `","name":"Test Board"}},"listen":{"on":true}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key, legacy
}

func TestProtectedLegacyMigration(t *testing.T) {
	path, key, _ := testProfile(t)
	store := &fakeKeyStore{keys: map[string][]byte{}}
	c, err := load(path, store, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	wantID := "00112233445566778899aabbccddeeff"
	if c.ClientID != wantID || !c.GetListen().On || c.Boards[testID].Name != "Test Board" ||
		!bytes.Equal(store.keys[testID], key) ||
		!slices.Equal(c.Match("0506"), []string{testID}) {
		t.Fatal("migration changed metadata or lost the board association")
	}
	assertKeylessConfig(t, path, key)
	c, err = load(path, store, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	boardID, _ := hex.DecodeString(testID)
	got := c.Key(boardID)
	if !bytes.Equal(got, key) || store.writes != 1 {
		t.Fatal("reload did not read the protected key")
	}
	got[0] = 0
	if !bytes.Equal(c.Key(boardID), key) {
		t.Fatal("caller mutated the cached key")
	}
}

func TestProtectedMigrationFailureKeepsOriginalConfig(t *testing.T) {
	for _, stage := range []string{"read", "write", "verify", "replace", "conflict"} {
		t.Run(stage, func(t *testing.T) {
			path, key, original := testProfile(t)
			store := &fakeKeyStore{keys: map[string][]byte{}}
			store.failRead = stage == "read"
			store.failWrite = stage == "write"
			store.badReadback = stage == "verify"
			otherKey := bytes.Repeat([]byte{7}, pairingKeySize)
			if stage == "conflict" {
				store.keys[testID] = otherKey
			}
			write := writeConfigFile
			if stage == "replace" {
				write = func(_ string, raw []byte) error {
					if bytes.Contains(raw, []byte(hex.EncodeToString(key))) {
						t.Fatal("attempted to write the plaintext key")
					}
					return fakeFailure
				}
			}
			c, err := load(path, store, write)
			if c != nil || err == nil || strings.Contains(err.Error(), hex.EncodeToString(key)) {
				t.Fatal("migration must return a safe error and no partial config")
			}
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, original) {
				t.Fatal("failed migration changed or lost original configuration")
			}
			if stage == "conflict" && !bytes.Equal(store.keys[testID], otherKey) {
				t.Fatal("migration overwrote a different protected key")
			}
			if stage == "replace" {
				if _, err := load(path, store, writeConfigFile); err != nil || store.writes != 1 {
					t.Fatal("a retry did not recover after config write failure")
				}
				assertKeylessConfig(t, path, key)
			}
		})
	}
}

func TestSetAndForgetProtectedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := &fakeKeyStore{keys: map[string][]byte{}}
	c, err := load(path, store, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	boardID, _ := hex.DecodeString(testID)
	key := bytes.Repeat([]byte{3}, pairingKeySize)
	if err := c.SetKey(boardID, key, "Test Board"); err != nil {
		t.Fatal(err)
	}
	assertKeylessConfig(t, path, key)
	replacement := bytes.Repeat([]byte{4}, pairingKeySize)
	c.writeConfig = func(string, []byte) error { return fakeFailure }
	if err := c.SetKey(boardID, replacement, "Different Name"); err == nil {
		t.Fatal("pair update accepted a failed file write")
	}
	if !bytes.Equal(store.keys[testID], key) || !bytes.Equal(c.Key(boardID), key) ||
		c.Boards[testID].Name != "Test Board" {
		t.Fatal("failed pair update did not preserve pairing")
	}
	if err := c.Forget(testID); err == nil || !bytes.Equal(store.keys[testID], key) {
		t.Fatal("failed metadata write lost the protected credential")
	}
	c.writeConfig = writeConfigFile
	store.failDelete = true
	if err := c.Forget(testID); err == nil || !bytes.Equal(c.Key(boardID), key) {
		t.Fatal("failed credential delete did not preserve pairing")
	}
	if _, err := load(path, store, writeConfigFile); err != nil {
		t.Fatal(err)
	}
	store.failDelete = false
	if err := c.Forget(testID); err != nil {
		t.Fatal(err)
	}
	if c.Key(boardID) != nil || len(c.Match("0506")) != 0 || store.keys[testID] != nil {
		t.Fatal("forget left pairing accessible")
	}
}

func TestFailedFirstPairingRollsBackCredential(t *testing.T) {
	for _, stage := range []string{"metadata", "readback", "cleanup", "existing credential"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			store := &fakeKeyStore{keys: map[string][]byte{}}
			c, err := load(path, store, writeConfigFile)
			if err != nil {
				t.Fatal(err)
			}
			var prior []byte
			if stage == "existing credential" {
				prior = bytes.Repeat([]byte{7}, pairingKeySize)
				store.keys[testID] = bytes.Clone(prior)
			}
			store.failDelete = stage == "cleanup"
			store.badReadback = stage == "readback"
			if !store.badReadback {
				c.writeConfig = func(string, []byte) error { return fakeFailure }
			}
			boardID, _ := hex.DecodeString(testID)
			key := bytes.Repeat([]byte{3}, pairingKeySize)
			err = c.SetKey(boardID, key, "Test Board")
			if err == nil || store.writes == 0 || c.Key(boardID) != nil || len(c.Match(testID)) != 0 {
				t.Fatal("failed first pairing changed in-memory state")
			}
			if store.failDelete {
				if !bytes.Equal(store.keys[testID], key) || !strings.Contains(err.Error(), "remove new pairing credential") {
					t.Fatal("pairing failure concealed credential cleanup failure")
				}
			} else if !bytes.Equal(store.keys[testID], prior) {
				t.Fatal("failed first pairing changed the protected credential")
			}
			assertKeylessConfig(t, path, key)
		})
	}
}

func TestNonWindowsConfigRetainsLegacyKeys(t *testing.T) {
	path, key, _ := testProfile(t)
	c, err := load(path, nil, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	boardID, _ := hex.DecodeString(testID)
	if !bytes.Equal(c.Key(boardID), key) {
		t.Fatal("legacy key not loaded")
	}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(raw, []byte(hex.EncodeToString(key))) {
		t.Fatal("legacy key format changed")
	}
}

func assertKeylessConfig(t *testing.T, path string, key []byte) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(hex.EncodeToString(key))) || bytes.Contains(raw, []byte(`"key"`)) {
		t.Fatal("plaintext pairing key was serialized")
	}
}
