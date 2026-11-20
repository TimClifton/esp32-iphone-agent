package daemon

import (
	"image"
	"os"
	"testing"
)

func TestRecordingOffCreatesNoHistory(t *testing.T) {
	t.Setenv("ESP32AGENT_RECORDING", "off")
	dir := t.TempDir()
	s := &Server{Dir: dir}
	s.recordShot(image.NewRGBA(image.Rect(0, 0, 10, 20)), 10, 20)
	s.recordAction(&Conn{}, Request{Cmd: "text", Text: "fixture text"}, nil)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 || s.hist != nil {
		t.Fatalf("recording-off wrote history: %v", err)
	}
}
