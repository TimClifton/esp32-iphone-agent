package daemon

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
)

func liveSnapshotConn() *Conn {
	c := newConn("0123456789ab", "test", "available")
	c.sess = &proto.Session{}
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"up":5000}`))
	return c
}

func snapshotImage() image.Image {
	return image.NewRGBA(image.Rect(0, 0, 10, 20))
}

func TestScreenshotRejectsCachedOldPicture(t *testing.T) {
	c := liveSnapshotConn()
	c.frame, c.captured = snapshotImage(), time.Now().Add(-time.Minute)
	img, err := c.waitFrame(context.Background(), c.videoGen, time.Time{}, 10*time.Millisecond, true)
	if err != errFrameLag || img != nil || !c.lastShot.IsZero() {
		t.Fatalf("old picture treated as current: image=%v error=%v", img != nil, err)
	}
}

func TestOldDecoderCannotPublishAfterReconnect(t *testing.T) {
	c := liveSnapshotConn()
	c.videoGen = 2
	c.onFrame(1, video.Frame{Img: snapshotImage(), TS: 4999})
	if c.Frame() != nil {
		t.Fatal("old decoder published into replacement session")
	}
}

func TestBoardRebootResetsCaptureClock(t *testing.T) {
	c := liveSnapshotConn()
	c.status["up"] = float64(100000)
	*c.offset = 1
	c.frame, c.captured = snapshotImage(), time.Now()
	oldGen := c.videoGen
	c.onStatus([]byte(`{"up":100}`))
	if c.offset == nil || *c.offset <= 1 || c.Frame() != nil || c.videoGen == oldGen {
		t.Fatal("board reboot retained old frame, decoder generation, or clock origin")
	}
	if c.recentVideoTimestamp(c.videoGen, 99) {
		t.Fatal("pre-reboot video flags certified a frame")
	}
}

func TestScreenshotAcceptsRecentlyCapturedPicture(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	if img, err := (&Server{}).frame(context.Background(), c); err != nil || img == nil || img != c.frame {
		t.Fatalf("fresh picture rejected: %v", err)
	}
	if !c.lastShot.Equal(c.captured) {
		t.Fatal("accepted screenshot timestamp did not identify the returned image")
	}
}

func snapshotSession(t *testing.T) (*proto.Session, proto.Link) {
	t.Helper()
	board := fakeBoard()
	l := board.Connect()
	t.Cleanup(func() { l.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	id := make([]byte, 16)
	key, _, err := proto.Pair(ctx, l, id)
	if err != nil {
		t.Fatal(err)
	}
	s, err := proto.Hello(ctx, l, id, func([]byte) []byte { return key })
	if err != nil {
		t.Fatal(err)
	}
	return s, l
}

func TestSnapshotRejectsDetachAfterReadiness(t *testing.T) {
	c := liveSnapshotConn()
	sess, _ := snapshotSession(t)
	c.sess = sess
	generation := c.videoGen
	c.onFrame(generation, video.Frame{Img: snapshotImage(), TS: 4999})
	if err := (&Server{}).videoState(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.detach(sess, errors.New("synthetic disconnection"))
	c.onFrame(generation, video.Frame{Img: snapshotImage(), TS: 4999})
	img, err := c.waitFrame(context.Background(), generation, time.Time{}, time.Second, true)
	if err != proto.ErrSession || img != nil || c.Frame() != nil || !c.lastShot.IsZero() {
		t.Fatalf("detached session yielded evidence: image=%v error=%v", img != nil, err)
	}
}

func TestSnapshotRejectsIdleWhileWaiting(t *testing.T) {
	c := liveSnapshotConn()
	generation := c.videoGen
	done := make(chan error, 1)
	go func() {
		_, err := c.waitFrame(context.Background(), generation, time.Time{}, time.Second, true)
		done <- err
	}()
	c.onStatus([]byte(`{"ap":"idle","fz":0,"up":5100}`))
	c.onFrame(generation, video.Frame{Img: snapshotImage(), TS: 5099})
	if err := <-done; err != errMirroring || !c.lastShot.IsZero() || c.Frame() != nil {
		t.Fatalf("idle stream yielded evidence: %v", err)
	}
}

func TestSnapshotReattachDiscardsStatusAndClock(t *testing.T) {
	c := liveSnapshotConn()
	oldGen := c.videoGen
	c.onFrame(oldGen, video.Frame{Img: snapshotImage(), TS: 4999})
	sess, l := snapshotSession(t)
	if err := c.attach(sess, "ble", l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.close)
	c.mu.Lock()
	clean := c.offset == nil && len(c.status) == 0 && c.statusAt.IsZero() &&
		c.videoStatusAt.IsZero() && c.videoGen != oldGen
	c.mu.Unlock()
	if !clean || c.Frame() != nil {
		t.Fatal("replacement session inherited old screenshot state")
	}
	c.onStatus([]byte(`{"ap":"streaming","fz":0,"up":100}`), sess)
	c.onFrame(oldGen, video.Frame{Img: snapshotImage(), TS: 99})
	if c.Frame() != nil {
		t.Fatal("replacement session accepted a late callback")
	}
}

func TestSnapshotRejectsStaleVideoStatusAfterUnrelatedUpdate(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	c.videoStatusAt = time.Now().Add(-3 * time.Second)
	c.onStatus([]byte(`{"owner":"cli","up":5100}`))
	img, err := c.waitFrame(context.Background(), c.videoGen, time.Time{}, time.Second, true)
	if img != nil || err != errVideoStatus || c.recentVideoTimestamp(c.videoGen, 5099) || !c.lastShot.IsZero() {
		t.Fatalf("old streaming status was treated as current: image=%v error=%v", img != nil, err)
	}
}

func TestFutureFrameRejected(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 5200})
	if c.Frame() != nil || c.recentVideoTimestamp(c.videoGen, 5200) {
		t.Fatal("future video timestamp accepted")
	}
}

func TestSnapshotRejectsFrameBeforeLatestAction(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	// Even if another request has already confirmed the action, this image
	// still needs to be independently newer than the action.
	c.lastAction = time.Now()
	c.lastShot = c.lastAction.Add(time.Millisecond)
	img, err := c.waitFrame(context.Background(), c.videoGen, time.Time{}, 10*time.Millisecond, true)
	if img != nil || err != errFrameLag || !c.lastShot.Equal(c.lastAction.Add(time.Millisecond)) {
		t.Fatalf("frame predating the action was accepted: image=%v error=%v", img != nil, err)
	}
}

func TestSnapshotRequiredForEveryInputInLiveProfile(t *testing.T) {
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "1")
	c := liveSnapshotConn()
	c.onStatus([]byte(`{"ap":"idle","fz":0,"up":5100}`))
	for _, cmd := range []string{"tap", "longpress", "swipe", "text", "keys", "button"} {
		res := (&Server{}).io(context.Background(), c, Request{Cmd: cmd, Text: "HOME"})
		if res.Status != "error" || res.Error != errMirroring.Error() {
			t.Errorf("%s did not require live mirroring: %+v", cmd, res)
		}
	}
}

func TestOperationRejectsCancellationAndReconnectionBeforeSend(t *testing.T) {
	c := liveSnapshotConn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	op, done, err := c.beginOperation(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	cancel()
	if _, err := c.Send(op, "click"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled operation reached wire send: %v", err)
	}
	c.mu.Lock()
	c.sess = &proto.Session{}
	c.resetSessionVideo()
	c.mu.Unlock()
	if _, err := c.Send(context.WithoutCancel(op), "press"); err != errVideoChanged {
		t.Fatalf("replacement session accepted old operation: %v", err)
	}
	if _, err := c.Send(context.WithoutCancel(op), "release"); err != errVideoChanged {
		t.Fatalf("cleanup crossed to another session: %v", err)
	}
}

func TestOperationDoesNotContinueHomeOrTextAfterMirroringStops(t *testing.T) {
	c := liveSnapshotConn()
	op, done, err := c.beginOperation(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	c.onStatus([]byte(`{"ap":"idle","fz":0,"up":5010}`))
	for _, command := range []string{"homescreen", "key cmd+h", "type example"} {
		if _, err := c.Send(op, command); err != errVideoChanged {
			t.Errorf("command %q continued after mirroring stopped: %v", command, err)
		}
	}
}

func TestOperationSerializesAndCancelPreventsQueuedInput(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	_, done, err := c.beginOperation(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan Response, 1)
	go func() { result <- (&Server{}).io(ctx, c, Request{Cmd: "keys", Text: "esc"}) }()
	res := <-result
	if res.Status != "error" || res.Error != context.DeadlineExceeded.Error() || c.nextID != 0 {
		t.Fatalf("canceled queued input was allowed to execute: %+v", res)
	}
	done()
	_, release, err := c.beginOperation(context.Background(), true)
	if err != nil {
		t.Fatalf("the next request couldn't acquire the board: %v", err)
	}
	release()
}

func TestOperationRejectsCoordinateAtExclusiveImageEdge(t *testing.T) {
	c := liveSnapshotConn()
	c.onFrame(c.videoGen, video.Frame{Img: snapshotImage(), TS: 4999})
	for _, pts := range [][]float64{{10, 0}, {0, 20}} {
		res := (&Server{}).io(context.Background(), c, Request{Cmd: "tap", Pts: pts})
		if res.Status != "error" || res.Error != "screen coordinates must be finite and within the current image" || c.nextID != 0 {
			t.Fatalf("the exact image edge was accepted: %+v", res)
		}
	}
}

func TestOperationLiveProfileDisablesRawMutation(t *testing.T) {
	t.Setenv("ESP32AGENT_REQUIRE_LIVE", "1")
	c := liveSnapshotConn()
	for _, command := range []string{"press", "move 10 0", "vdbg press", "vdbg\npress"} {
		if res := (&Server{}).rawSend(context.Background(), c, command); res.Status != "error" || c.nextID != 0 {
			t.Errorf("raw mutation %q reached the board: %+v", command, res)
		}
	}
}
