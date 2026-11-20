package daemon

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
)

func TestRefreshScreenRejectsUnsafeStatusWithoutMotion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		stale  string
		want   error
	}{
		{"stale mirror", `{"ap":"streaming","fz":0,"hid":"ok","hidm":"ble"}`, "video", errVideoStatus},
		{"frozen", `{"ap":"streaming","fz":1,"hid":"ok","hidm":"ble"}`, "", errFrozen},
		{"idle", `{"ap":"idle","fz":0,"hid":"ok","hidm":"ble"}`, "", errMirroring},
		{"disconnected pointer", `{"ap":"streaming","fz":0,"hid":"off","hidm":"ble"}`, "", errRefreshHID},
		{"stale pointer", `{"ap":"streaming","fz":0,"hid":"ok","hidm":"ble"}`, "hid", errRefreshHID},
		{"USB pointer", `{"ap":"streaming","fz":0,"hid":"ok","hidm":"usb"}`, "", errRefreshHID},
		{"unknown mode", `{"ap":"streaming","fz":0,"hid":"ok","hidm":"other"}`, "", errRefreshHID},
		{"missing mode", `{"ap":"streaming","fz":0,"hid":"ok"}`, "", errRefreshHID},
		{"partial update over prior BLE", `{"ap":"streaming","fz":0,"hid":"ok","hidm":"ble"}`, "partial", errRefreshHID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, board := restartStreamFixture(t, func(string) string { return "ok" })
			c.onStatus([]byte(tc.status))
			switch tc.stale {
			case "video":
				c.videoStatusAt = time.Now().Add(-3 * time.Second)
			case "hid":
				c.hidStatusAt = time.Now().Add(-3 * time.Second)
				c.onStatus([]byte(`{"ap":"streaming","fz":0}`))
			case "partial":
				c.onStatus([]byte(`{"ap":"streaming","fz":0,"hid":"ok"}`))
			}
			res := (&Server{}).refreshScreen(context.Background(), c)
			if res.Status != "error" || !strings.Contains(res.Error, tc.want.Error()) || len(board.Commands) != 0 {
				t.Fatalf("unsafe readiness caused movement: %+v commands=%q", res, board.Commands)
			}
		})
	}
}

func TestRefreshScreenRejectsCanceledRequestAndChangedSessionBeforeMotion(t *testing.T) {
	c, board := restartStreamFixture(t, func(string) string { return "ok" })
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"hid":"ok","hidm":"ble"}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res := (&Server{}).refreshScreen(ctx, c); res.Status != "error" || len(board.Commands) != 0 {
		t.Fatalf("canceled refresh sent movement: %+v commands=%q", res, board.Commands)
	}
	op, release, err := c.beginOperation(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	binding := op.Value(operationKey{}).(operationBinding)
	binding.refresh = true
	op = context.WithValue(op, operationKey{}, binding)
	if _, err := c.Send(op, "press"); err == nil {
		t.Fatal("refresh binding bypassed the pointer-only allowlist")
	}
	c.mu.Lock()
	c.sess = &proto.Session{}
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.sess = binding.session; c.mu.Unlock() }()
	if _, err := c.Send(op, "move 1 0"); err != errVideoChanged || len(board.Commands) != 0 {
		t.Fatalf("refresh moved the pointer on a replacement session: %v", err)
	}
}

func TestRefreshScreenRequiresPostCommandFrameAndReturnsPointer(t *testing.T) {
	moved := make(chan struct{})
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "move 1 0" {
			close(moved)
		}
		return "ok"
	})
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"hid":"connected","hidm":"ble"}`))
	c.captured = time.Now().Add(-8 * time.Second)
	gen := c.videoGen
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan Response, 1)
	go func() { done <- (&Server{}).refreshScreen(ctx, c) }()
	select {
	case <-moved:
	case res := <-done:
		t.Fatalf("refresh did not attempt a nudge: %+v", res)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case res := <-done:
		t.Fatalf("old screen incorrectly verified the refresh: %+v", res)
	case <-time.After(20 * time.Millisecond):
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case res := <-done:
			if res.Status != "ok" || !slices.Equal(board.Commands, []string{"move 1 0", "move -1 0"}) {
				t.Fatalf("unexpected refresh result: %+v commands=%q", res, board.Commands)
			}
			return
		case <-tick.C:
			c.onFrame(gen, restartFrame(c))
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestRefreshScreenNeverReturnsPointerAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, board := restartStreamFixture(t, func(cmd string) string {
		if cmd == "move 1 0" {
			cancel()
		}
		return "ok"
	})
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"hid":"ok","hidm":"ble"}`))
	res := (&Server{}).refreshScreen(ctx, c)
	if res.Status != "error" || !slices.Equal(board.Commands, []string{"move 1 0"}) {
		t.Fatalf("refresh continued after cancellation: %+v commands=%q", res, board.Commands)
	}
}
