package ble

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessAdapterUsesConfiguredRealBoardLabel(t *testing.T) {
	t.Setenv("ESP32AGENT_PIPE_NAME", "ESP32 Agent 6D05")
	bin := filepath.Join(t.TempDir(), "fixture-adapter")
	if err := os.WriteFile(bin, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	found, err := (&Sim{Bin: bin}).Scan(context.Background(), time.Millisecond)
	if err != nil || len(found) != 1 || found[0].Name != "ESP32 Agent 6D05" || found[0].Addr != "pipe" {
		t.Fatalf("wrong adapter identity: %v %v", found, err)
	}
}
