package config

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

var (
	advapi32   = windows.NewLazySystemDLL("advapi32.dll")
	credRead   = advapi32.NewProc("CredReadW")
	credWrite  = advapi32.NewProc("CredWriteW")
	credDelete = advapi32.NewProc("CredDeleteW")
	credFree   = advapi32.NewProc("CredFree")
)

// windowsCredential matches CREDENTIALW from wincred.h.
type windowsCredential struct {
	flags          uint32
	kind           uint32
	targetName     *uint16
	comment        *uint16
	lastWritten    windows.Filetime
	blobSize       uint32
	blob           *byte
	persist        uint32
	attributeCount uint32
	attributes     unsafe.Pointer
	targetAlias    *uint16
	userName       *uint16
}

// credentialManager uses the current Windows user's local credential set.
type credentialManager struct {
	targetPrefix string
}

func platformKeyStore(dir string) (keyStore, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, err
	}
	return credentialManager{
		targetPrefix: fmt.Sprintf("ESP32iPhoneAgent/pairing/%x/", sha256.Sum256([]byte(resolved))),
	}, nil
}

func (s credentialManager) Read(id string) ([]byte, error) {
	target, err := windows.UTF16PtrFromString(s.targetPrefix + id)
	if err != nil {
		return nil, err
	}
	var credential *windowsCredential
	result, _, callErr := credRead.Call(
		uintptr(unsafe.Pointer(target)), credTypeGeneric, 0, uintptr(unsafe.Pointer(&credential)),
	)
	if result == 0 {
		if errors.Is(callErr, windows.ERROR_NOT_FOUND) {
			return nil, nil
		}
		return nil, callErr
	}
	defer func() { credFree.Call(uintptr(unsafe.Pointer(credential))) }()
	if credential.blobSize != pairingKeySize || credential.blob == nil {
		return nil, errors.New("invalid protected pairing credential size")
	}
	return bytes.Clone(unsafe.Slice(credential.blob, pairingKeySize)), nil
}

func (s credentialManager) Write(id string, key []byte) error {
	target, err := windows.UTF16PtrFromString(s.targetPrefix + id)
	if err != nil {
		return err
	}
	user, err := windows.UTF16PtrFromString("ESP32 iPhone Agent")
	if err != nil {
		return err
	}
	credential := windowsCredential{
		kind: credTypeGeneric, targetName: target, userName: user,
		blobSize: uint32(len(key)), blob: unsafe.SliceData(key), persist: credPersistLocalMachine,
	}
	result, _, callErr := credWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(credential)
	if result == 0 {
		return callErr
	}
	return nil
}

func (s credentialManager) Delete(id string) error {
	target, err := windows.UTF16PtrFromString(s.targetPrefix + id)
	if err != nil {
		return err
	}
	result, _, callErr := credDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	if result == 0 && !errors.Is(callErr, windows.ERROR_NOT_FOUND) {
		return callErr
	}
	return nil
}

func replaceConfigFile(source, path string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
