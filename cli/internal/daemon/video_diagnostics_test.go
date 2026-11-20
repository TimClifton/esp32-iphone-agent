package daemon

import (
	"encoding/binary"
	"maps"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
)

func TestVideoDiagnosticsCountsAUsAndFreshnessDecisions(t *testing.T) {
	c := liveSnapshotConn()
	c.ffmpeg = ""
	c.re = video.NewReassembler()
	captureNow := time.Now()
	offset := captureNow.UnixMilli() - 5000
	c.offset = &offset
	c.restartAfter = captureNow.Add(-50 * time.Millisecond)
	gen := c.videoGen
	au := binary.LittleEndian.AppendUint32([]byte{0, 0, video.Start | video.End | video.IDR}, 4999)
	c.onVideo(append(au, 1), false)

	for _, probe := range []struct {
		gen int
		ts  uint32
	}{
		{gen - 1, 4999}, {gen, 0}, {gen, 1000}, {gen, 6000}, {gen, 4900},
	} {
		if captured := c.captureTime(probe.gen, probe.ts, captureNow); !captured.IsZero() {
			t.Fatalf("rejected frame became usable: %+v", probe)
		}
	}
	c.status["ap"] = "idle"
	if captured := c.captureTime(gen, 4999, captureNow); !captured.IsZero() {
		t.Fatal("idle status allowed a frame")
	}
	c.status["ap"] = "streaming"
	c.onFrame(gen, video.Frame{TS: 4999, Img: snapshotImage()})
	stats := c.videoDiagnostics()
	rejected := stats["rejected_frames"].(map[string]uint64)
	want := map[string]uint64{"generation": 1, "clock": 1, "old": 1, "future": 1, "pre_restart": 1, "status": 1}
	if stats["complete_picture_aus"] != uint64(1) || stats["last_picture_au_ts_ms"] != uint32(4999) ||
		stats["last_picture_au_age_ms"] == nil || stats["published_frames"] != uint64(1) ||
		stats["last_published_age_ms"] == nil || !maps.Equal(rejected, want) {
		t.Fatalf("inconsistent video diagnostics: %+v", stats)
	}
	rejected["old"]++
	if !maps.Equal(c.videoDiagnostics()["rejected_frames"].(map[string]uint64), want) {
		t.Fatal("diagnostics exposed the mutable counter map")
	}
	c.resetVideo()
	if reset := c.videoDiagnostics(); reset["generation"] != gen+1 || reset["published_frames"] != uint64(0) ||
		len(reset["rejected_frames"].(map[string]uint64)) != 0 {
		t.Fatalf("new generation inherited stale diagnostics: %+v", reset)
	}
}
