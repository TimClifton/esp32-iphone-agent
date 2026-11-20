package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/pointer"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
)

func TestScrollValidatesBeforeBoardAccess(t *testing.T) {
	s := &Server{}
	for _, delta := range []int{-11, 0, 11} {
		res := s.Do(context.Background(), Request{Cmd: "scroll", Delta: delta})
		if res.Status != "error" || res.Error != pointer.ErrScrollDelta.Error() {
			t.Fatalf("invalid delta reached board lookup: %+v", res)
		}
	}
}

func TestScrollSendsOneBoundedVerticalWheelReport(t *testing.T) {
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "1")
	t.Setenv("ESP32AGENT_RECORDING", "off")
	for _, tc := range []struct {
		delta int
		want  string
	}{{-10, "scroll -10 0"}, {10, "scroll 10 0"}} {
		c, board := restartStreamFixture(t, func(string) string { return "ok" })
		s := &Server{Dir: t.TempDir(), conns: map[string]*Conn{c.ID: c}}
		res := s.Do(context.Background(), Request{Cmd: "scroll", Delta: tc.delta})
		if res.Status != "ok" || !slices.Equal(board.Commands, []string{tc.want}) {
			t.Fatalf("unexpected scroll result: %+v commands=%q", res, board.Commands)
		}
		if message := res.Data.(map[string]string)["message"]; !strings.Contains(message, "acknowledged") ||
			!strings.Contains(message, "verify") {
			t.Fatalf("acknowledgment implied unverified screen movement: %q", message)
		}
	}
}

func TestScrollRequiresFreshScreenAndRefusesCanceledOrReplacedSession(t *testing.T) {
	c, board := restartStreamFixture(t, func(string) string { return "ok" })
	s := &Server{Dir: t.TempDir(), conns: map[string]*Conn{c.ID: c}}
	req := Request{Cmd: "scroll", Delta: 2}
	c.captured = time.Now().Add(-3 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	if res := s.Do(ctx, req); res.Status != "error" || len(board.Commands) != 0 {
		t.Fatalf("stale screenshot allowed scrolling: %+v commands=%q", res, board.Commands)
	}
	cancel()
	if res := s.Do(ctx, req); res.Status != "error" || len(board.Commands) != 0 {
		t.Fatalf("canceled scroll reached the board: %+v commands=%q", res, board.Commands)
	}
	op, done, err := c.beginOperation(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	c.mu.Lock()
	session := c.sess
	c.sess = &proto.Session{}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.sess = session; c.mu.Unlock() }()
	if err := c.pointer(s.Dir).Scroll(op, req.Delta); err != errVideoChanged || len(board.Commands) != 0 {
		t.Fatalf("scroll crossed into a replacement session: %v commands=%q", err, board.Commands)
	}
}
