//go:build !windows

package config

import "os"

func platformKeyStore(string) (keyStore, error) { return nil, nil }

func replaceConfigFile(source, path string) error { return os.Rename(source, path) }
