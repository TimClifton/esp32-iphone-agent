package daemon

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
)

func restartFrame(c *Conn) video.Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := uint32(time.Now().Add(-time.Millisecond).UnixMilli() - *c.offset)
	return video.Frame{Img: snapshotImage(), TS: ts}
}

func restartStreamFixture(t *testing.T, handle func(string) string) (*Conn, *proto.FakeBoard) {
	t.Helper()
	board := fakeBoard()
	board.Handle = handle
	link := board.Connect()
	t.Cleanup(func() { link.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id := make([]byte, 16)
	key, _, err := proto.Pair(ctx, link, id)
	if err != nil {
		t.Fatal(err)
	}
	session, err := proto.Hello(ctx, link, id, func([]byte) []byte { return key })
	if err != nil {
		t.Fatal(err)
	}
	c := liveSnapshotConn()
	if err := c.attach(session, "ble", link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"up":5000}`), session)
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	return c, board
}

func TestRestartStreamResetsHostOnceBeforeOneContinuousReplay(t *testing.T) {
	diagnostics := 0
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "vdbg" {
			diagnostics++
			if diagnostics < 3 {
				return "ok fz0 fg1 nx5/10 cu1"
			}
			return "ok fz0 fg0 nx10/10 cu0"
		}
		return "ok"
	})
	session, generation, assembler := c.sess, c.videoGen, c.re
	res := (&Server{}).restartStream(context.Background(), c)
	if res.Status != "ok" || c.sess != session || c.videoGen != generation+1 || c.re == assembler || c.Frame() != nil {
		t.Fatalf("restart did not replace exactly one decoder generation on the original session: %+v", res)
	}
	c.onFrame(generation, video.Frame{Img: snapshotImage(), TS: 4999})
	if c.Frame() != nil {
		t.Fatal("old decoder published after the restart")
	}
	if _, err := c.Send(context.Background(), "snap"); err != errStreamRestarting {
		t.Fatalf("snapshot could duplicate the initial replay: %v", err)
	}
	want := []string{"vdbg", "stream off", "vdbg", "vdbg", "stream on"}
	if !slices.Equal(board.Commands, want) {
		t.Fatalf("got wire commands %q, want %q", board.Commands, want)
	}
	c.onFrame(generation+1, video.Frame{Img: snapshotImage(), TS: 4999})
	if c.Frame() != nil || !c.waitingForReplay {
		t.Fatal("cached picture captured shortly before restart completed the replay")
	}
	time.Sleep(4 * time.Millisecond)
	c.onFrame(generation+1, restartFrame(c))
	if c.Frame() == nil || c.waitingForReplay {
		t.Fatal("a verified new-generation frame did not complete the replay")
	}
}

func TestRestartStreamRejectsFramesBeforeStreamOnAcknowledgment(t *testing.T) {
	started := make(chan struct{})
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "stream on" {
			close(started)
			return "" // let the test confirm the gate before acknowledging
		}
		return "ok fg0"
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan Response, 1)
	go func() { done <- (&Server{}).restartStream(ctx, c) }()
	select {
	case <-started:
	case res := <-done:
		t.Fatalf("restart returned before stream on: %+v", res)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	c.mu.Lock()
	gen, id := c.videoGen, c.nextID
	c.mu.Unlock()
	frame := restartFrame(c)
	c.onFrame(gen, frame)
	if c.Frame() != nil || c.Live() {
		t.Fatal("unacknowledged restart published nominally recent video")
	}
	board.Reply(strconv.Itoa(id) + " ok")
	if res := <-done; res.Status != "ok" {
		t.Fatalf("stream on acknowledgment failed: %+v", res)
	}
	c.onFrame(gen, frame)
	if c.Frame() != nil || c.Live() {
		t.Fatal("acknowledged restart accepted a frame captured before its ACK")
	}
}

func TestRestartStreamRejectsUnsupportedCursorAndOffFailure(t *testing.T) {
	for _, expected := range [][]string{{"vdbg"}, {"vdbg", "stream off"}} {
		c, board := restartStreamFixture(t, func(cmd string) string {
			if cmd == "vdbg" && len(expected) == 2 {
				return "ok fg1"
			}
			return "err unsupported command"
		})
		gen, before := c.videoGen, c.Frame()
		res := (&Server{}).restartStream(context.Background(), c)
		if res.Status != "error" || c.videoGen != gen || c.Frame() != before || c.streamPaused ||
			!slices.Equal(board.Commands, expected) {
			t.Fatalf("restart changed state after a rejected command: %+v, commands=%q", res, board.Commands)
		}
	}
}

func TestRestartStreamFailsClosedOnOffTimeoutAndCanRetry(t *testing.T) {
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "stream off" {
			return "" // timed out without knowing whether the board applied it
		}
		return "ok fg0"
	})
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	gen := c.videoGen
	res := (&Server{}).restartStream(ctx, c)
	if res.Status != "error" || !strings.Contains(res.Error, "may be paused") || c.videoGen != gen ||
		!slices.Equal(board.Commands, []string{"vdbg", "stream off"}) {
		t.Fatalf("ambiguous off triggered further commands: %+v, commands=%q", res, board.Commands)
	}
	if _, err := (&Server{}).frame(context.Background(), c); err != errStreamPaused {
		t.Fatalf("snapshot could accept old video after ambiguous off: %v", err)
	}
	board.Handle = func(string) string { return "ok fg0" }
	if res := (&Server{}).restartStream(context.Background(), c); res.Status != "ok" || c.streamPaused {
		t.Fatalf("explicit restart could not recover a paused stream: %+v", res)
	}
}

func TestRestartStreamCancellationAfterOffDoesNotResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "vdbg" {
			calls++
			if calls == 2 {
				cancel()
			}
		}
		return "ok fg1"
	})
	res := (&Server{}).restartStream(ctx, c)
	if res.Status != "error" || !c.streamPaused || !slices.Equal(board.Commands, []string{"vdbg", "stream off", "vdbg"}) {
		t.Fatalf("cancel did not leave video safely paused: %+v, commands=%q", res, board.Commands)
	}
}
