package config

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCredentialTargetIsScopedToProfile(t *testing.T) {
	firstDir := t.TempDir()
	first, err := platformKeyStore(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	alias, err := platformKeyStore(firstDir + string(filepath.Separator) + ".")
	if err != nil {
		t.Fatal(err)
	}
	second, err := platformKeyStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first != alias || first == second {
		t.Fatal("credential target was inconsistent or shared between profiles")
	}
}

func TestNativeWindowsCredentialRoundTrip(t *testing.T) {
	if os.Getenv("ESP32AGENT_TEST_NATIVE_CREDENTIALS") != "1" {
		t.Skip("native test requires permission to write a disposable credential")
	}
	dir := t.TempDir()
	secrets, err := platformKeyStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := secrets.Read(testID)
	if err != nil {
		t.Fatalf("check disposable credential: %v", err)
	}
	if entry != nil {
		t.Fatal("disposable credential target unexpectedly exists")
	}
	t.Cleanup(func() {
		if err := secrets.Delete(testID); err != nil {
			t.Errorf("clean up disposable credential: %v", err)
		}
	})
	path := filepath.Join(dir, "config.json")
	c, err := load(path, secrets, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, pairingKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	boardID, _ := hex.DecodeString(testID)
	if err := c.SetKey(boardID, key, "Native Test Board"); err != nil {
		t.Fatal(err)
	}
	assertKeylessConfig(t, path, key)
	c, err = load(path, secrets, writeConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c.Key(boardID), key) {
		t.Fatal("reloaded config lost the disposable credential")
	}
	if err := c.Forget(testID); err != nil {
		t.Fatal(err)
	}
	entry, err = secrets.Read(testID)
	if err != nil || entry != nil {
		t.Fatal("disposable credential remains after unpair")
	}
}
