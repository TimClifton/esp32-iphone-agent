package daemon

import (
	"context"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
	"math"
	"strings"
	"testing"
	"time"
)

func TestMalformedCoordinatesFailBeforeInput(t *testing.T) {
	t.Setenv("ESP32AGENT_RECORDING", "off")
	for _, points := range [][]float64{{1}, {math.NaN(), 2}, {math.Inf(1), 2}, {-1, 2}, {11, 2}} {
		c := liveSnapshotConn()
		c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r := (&Server{}).io(ctx, c, Request{Cmd: "tap", Pts: points})
		cancel()
		if r.Status != "error" || (!strings.Contains(r.Error, "coordinates") && !strings.Contains(r.Error, "coordinate")) {
			t.Fatalf("bad points accepted or reached device: %+v", r)
		}
	}
}
