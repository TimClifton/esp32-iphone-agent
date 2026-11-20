package daemon

import (
	"testing"
	"time"
)

func TestConnectionTimeoutIsBounded(t *testing.T) {
	t.Setenv("ESP32AGENT_SIM", "fixture-pipe-adapter")
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{{"", 15 * time.Second}, {"45s", 45 * time.Second}, {"0s", 15 * time.Second}, {"5m", 15 * time.Second}, {"invalid", 15 * time.Second}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("ESP32AGENT_CONNECT_TIMEOUT", tc.value)
			if got := connectionTimeout(); got != tc.want {
				t.Fatalf("got%v want%v", got, tc.want)
			}
		})
	}
}

func TestNativeConnectionTimeoutUnchanged(t *testing.T) {
	t.Setenv("ESP32AGENT_SIM", "")
	t.Setenv("ESP32AGENT_CONNECT_TIMEOUT", "45s")
	if got := connectionTimeout(); got != 15*time.Second {
		t.Fatalf("native adapter budget changed: %v", got)
	}
}
