package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/config"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/pointer"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
	"golang.org/x/image/draw"
)

var (
	errMirroring        = errors.New("screen mirroring is off: on the iPhone open Control Center > Screen Mirroring > ESP32 Agent")
	errFrozen           = errors.New("video frozen: restart Screen Mirroring on the iPhone (Control Center > Screen Mirroring > Stop Mirroring, then ESP32 Agent)")
	errVideoStatus      = errors.New("the board has not sent recent video status")
	errVideoChanged     = errors.New("video session changed; retry the screenshot")
	errFrameLag         = errors.New("decoded screen has not caught up; wait and retry the screenshot")
	errActionFrame      = errors.New("no fresh screen after the action; navigation result is unverified")
	errStreamPaused     = errors.New("desktop video is paused; run device restart-stream again")
	errStreamRestarting = errors.New("desktop video restart or replay is in progress; retry the screenshot")
	errRefreshHID       = errors.New("screen refresh requires a recent report of a connected BLE pointer")
)

func num(v any) float64 { f, _ := v.(float64); return f }

// videoState explains why there is no usable screen, nil when mirroring streams. It must hold mu.
func (c *Conn) videoState(now time.Time) error {
	if c.sess == nil {
		return proto.ErrSession
	}
	if c.streamPaused {
		return errStreamPaused
	}
	if !currentVideoTime(now, c.videoStatusAt) {
		return errVideoStatus
	}
	if c.status["ap"] != "streaming" {
		return errMirroring
	}
	if c.status["fz"] != float64(0) {
		return errFrozen
	}
	return nil
}

func (s *Server) videoState(ctx context.Context, c *Conn) error {
	var ff string
	for i := 0; ; i++ { // the first status arrives within a second of connecting
		c.mu.Lock()
		err := c.videoState(time.Now())
		ff = c.ffmpeg
		c.mu.Unlock()
		if err == nil {
			break
		}
		if err != errVideoStatus || i == 30 {
			return err
		}
		if err := sleepCtx(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	if ff == "" {
		_, err := video.FFmpeg()
		if err == nil {
			err = errors.New("ffmpeg could not decode the video (see daemon.log)")
		}
		return err
	}
	return nil
}

// frame returns the screen as it is after the last action (like the app's VideoFeed.afterAction).
func (s *Server) frame(ctx context.Context, c *Conn) (image.Image, error) {
	if err := s.videoState(ctx, c); err != nil {
		return nil, err
	}
	c.mu.Lock()
	generation := c.videoGen
	afterAction := c.lastAction.After(c.lastShot)
	c.mu.Unlock()
	if binding, ok := ctx.Value(operationKey{}).(operationBinding); ok {
		generation = binding.generation
	}
	if afterAction {
		c.Settled(ctx, 500*time.Millisecond, 300*time.Millisecond, 3*time.Second)
	}
	img, err := c.waitFrame(ctx, generation, time.Time{}, 5*time.Second, true)
	if err != errFrameLag {
		return img, err
	}

	c.mu.Lock()
	waitingForDecoder := c.dec == nil && c.frame == nil && !c.streamRestartOwnsReplay()
	stateErr := c.videoState(time.Now())
	sameVideo := c.videoGen == generation
	afterAction = c.lastAction.After(c.lastShot)
	c.mu.Unlock()
	if stateErr != nil {
		return nil, stateErr
	}
	if !sameVideo {
		return nil, errVideoChanged
	}
	if waitingForDecoder {
		// Nothing streamed yet. Ask for the current GOP only if a decoder
		// isn't already replaying it.
		sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_, err = c.Send(sctx, "snap")
		cancel()
		if err != nil {
			return nil, fmt.Errorf("no video from the board: %w", err)
		}
		img, err = c.waitFrame(ctx, generation, time.Time{}, 5*time.Second, true)
	}
	if err == errFrameLag && afterAction {
		return nil, errActionFrame
	}
	return img, err
}

// streamRestartOwnsReplay must hold mu.
func (c *Conn) streamRestartOwnsReplay() bool {
	return c.restartingStream || c.streamPaused || c.waitingForReplay
}

func (c *Conn) videoCursor(ctx context.Context) (following bool, err error) {
	reply, err := c.Send(ctx, "vdbg")
	if err != nil {
		return false, err
	}
	for _, field := range strings.Fields(reply) {
		switch field {
		case "fg0":
			return false, nil
		case "fg1":
			return true, nil
		}
	}
	return false, errors.New("board did not report its desktop video cursor")
}

func (c *Conn) pauseDesktopStream(session *proto.Session) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != session {
		return false
	}
	c.streamPaused = true
	c.waitingForReplay = false
	c.signalVideo()
	return true
}

func (s *Server) restartStream(ctx context.Context, c *Conn) Response {
	ctx, done, err := c.beginOperation(ctx, false)
	if err != nil {
		return fail(err)
	}
	defer done()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	binding := ctx.Value(operationKey{}).(operationBinding)
	c.mu.Lock()
	if c.sess != binding.session {
		c.mu.Unlock()
		return fail(errVideoChanged)
	}
	c.restartingStream = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.sess == binding.session {
			c.restartingStream = false
		}
		c.mu.Unlock()
	}()

	// A firmware without the cursor diagnostic may not provide a reliable
	// restart barrier. Check before turning off the current stream.
	if _, err := c.videoCursor(ctx); err != nil {
		return fail(fmt.Errorf("desktop stream restart unavailable: %w", err))
	}
	if _, err := c.Send(ctx, "stream off"); err != nil {
		if ctx.Err() != nil && c.pauseDesktopStream(binding.session) {
			return fail(fmt.Errorf("desktop stream may be paused; retry device restart-stream: %w", err))
		}
		return fail(fmt.Errorf("desktop stream off was not confirmed: %w", err))
	}
	if !c.pauseDesktopStream(binding.session) {
		return fail(errVideoChanged)
	}
	pausedCtx, pausedCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pausedCancel()
	for {
		following, err := c.videoCursor(pausedCtx)
		if err != nil {
			return fail(fmt.Errorf("desktop stream paused, but cursor reset was not confirmed: %w", err))
		}
		if !following {
			break
		}
		if err := sleepCtx(pausedCtx, 100*time.Millisecond); err != nil {
			return fail(fmt.Errorf("desktop stream paused, but cursor reset was not confirmed: %w", err))
		}
	}
	c.mu.Lock()
	if err := c.operationError(ctx, "stream on"); err != nil {
		c.mu.Unlock()
		return fail(err)
	}
	c.resetVideo()
	c.waitingForReplay = true // receive CONFIG/IDR, but don't publish before stream-on ACK
	binding.generation = c.videoGen
	c.mu.Unlock()

	// With its cursor reset, the board automatically starts with CONFIG and
	// the latest IDR. Asking for snap would replay that same GOP a second time.
	ctx = context.WithValue(ctx, operationKey{}, binding)
	if _, err := c.Send(ctx, "stream on"); err != nil {
		c.pauseDesktopStream(binding.session)
		return fail(fmt.Errorf("desktop stream on was not confirmed; retry device restart-stream: %w", err))
	}
	c.mu.Lock()
	if c.sess != binding.session {
		c.mu.Unlock()
		return fail(errVideoChanged)
	}
	c.restartAfter = time.Now()
	c.streamPaused = false
	c.signalVideo()
	c.mu.Unlock()
	return ok(map[string]string{"message": "desktop stream restart requested; take a new screenshot to verify"})
}

func (s *Server) refreshScreen(ctx context.Context, c *Conn) Response {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	ctx, done, err := c.beginOperation(ctx, false)
	if err != nil {
		return fail(err)
	}
	defer done()
	binding := ctx.Value(operationKey{}).(operationBinding)
	binding.refresh = true
	ctx = context.WithValue(ctx, operationKey{}, binding)

	c.mu.Lock()
	ptr := c.ptr
	c.mu.Unlock()
	if ptr != nil {
		ptr.Forget() // This relative move does not update the calibrated pointer's position.
	}
	if _, err := c.Send(ctx, "move 1 0"); err != nil {
		return fail(fmt.Errorf("pointer refresh was not acknowledged: %w", err))
	}
	_, frameErr := c.waitFrame(ctx, binding.generation, time.Now(), 3*time.Second, false)
	// The same binding checks the session, cancellation and live status again
	// at the wire. It prevents restoring the pointer on a replacement link.
	if _, err := c.Send(ctx, "move -1 0"); err != nil {
		return fail(fmt.Errorf("pointer refresh moved the pointer, but the return move was not confirmed: %w", err))
	}
	if frameErr != nil {
		return fail(fmt.Errorf("pointer refresh was requested, but a new frame was not verified: %w", frameErr))
	}
	return ok(map[string]string{"message": "fresh screen verified after pointer-only refresh"})
}

func encode(img image.Image, format string, quality int, scale float64, maxSize int) ([]byte, image.Rectangle, error) {
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	if scale <= 0 || scale > 1 {
		scale = 1
	}
	if maxSize > 0 && max(w, h)*scale > float64(maxSize) {
		scale = float64(maxSize) / max(w, h)
	}
	if scale < 1 {
		dst := image.NewRGBA(image.Rect(0, 0, max(1, int(w*scale+0.5)), max(1, int(h*scale+0.5))))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	var err error
	switch format {
	case "", "png":
		err = (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img)
	case "jpeg", "jpg":
		if quality <= 0 || quality > 100 {
			quality = 90
		}
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	default:
		err = fmt.Errorf("format must be png or jpeg")
	}
	return buf.Bytes(), img.Bounds(), err
}

func (s *Server) screenshot(ctx context.Context, c *Conn, req Request) Response {
	img, err := s.frame(ctx, c)
	if err != nil {
		return fail(err)
	}
	data, r, err := encode(img, req.Format, req.Quality, req.Scale, req.MaxSize)
	if err != nil {
		return fail(err)
	}
	format := "png"
	if req.Format == "jpeg" || req.Format == "jpg" {
		format = "jpeg"
	}
	s.recordShot(img, r.Dx(), r.Dy())
	return ok(map[string]any{"format": format, "data": base64.StdEncoding.EncodeToString(data),
		"width": r.Dx(), "height": r.Dy(), "device": c.ID})
}

func (c *Conn) pointer(dir string) *pointer.Pointer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ptr == nil {
		c.ptr = pointer.New(device{c}, c, config.CalibrationPath(dir, c.ID))
	}
	return c.ptr
}

func requireLive() bool { return os.Getenv("ESP32AGENT_REQUIRE_LIVE") == "1" }

func (s *Server) rawSend(ctx context.Context, c *Conn, command string) Response {
	if requireLive() {
		if command != "vdbg" {
			return fail(errors.New("raw board commands are disabled in the live phone testing profile"))
		}
	} else {
		op, done, err := c.beginOperation(ctx, false)
		if err != nil {
			return fail(err)
		}
		defer done()
		defer c.pointer(s.Dir).Forget()
		ctx = op
	}
	reply, err := c.Send(ctx, command)
	if err != nil {
		return fail(err)
	}
	return ok(map[string]string{"reply": reply})
}

// size provides the screen dimensions for recorded action metadata.
func (c *Conn) size() (float64, float64, error) {
	if f := c.Frame(); f != nil {
		return float64(f.Bounds().Dx()), float64(f.Bounds().Dy()), nil
	}
	st, _ := c.Status()
	if w, h := num(st["w"]), num(st["h"]); w > 0 && h > 0 {
		return w, h, nil
	}
	return 0, 0, errors.New("screen size unknown (no video yet): take a screenshot first")
}

func (s *Server) calibrate(ctx context.Context, c *Conn) Response {
	ctx, done, err := c.beginOperation(ctx, true)
	if err != nil {
		return fail(err)
	}
	defer done()
	if _, err := s.frame(ctx, c); err != nil {
		return fail(err)
	}
	p := c.pointer(s.Dir)
	p.RequireLive = requireLive()
	if p.RequireLive {
		residual, err := p.CalibrateVerified(ctx)
		c.noteAction()
		if err != nil {
			return fail(err)
		}
		return ok(map[string]any{"span": map[string]int{"w": p.Span.W, "h": p.Span.H}, "error": residual})
	}
	if err := p.Calibrate(ctx); err != nil {
		c.noteAction()
		return fail(err)
	}
	out := map[string]any{"span": map[string]int{"w": p.Span.W, "h": p.Span.H}}
	if e, found, err := p.Verify(ctx, 0.5, 0.62); err == nil && found {
		out["error"] = e // miss at a check point, fraction of the screen
	}
	c.noteAction()
	return ok(out)
}

func (c *Conn) noteAction() {
	c.mu.Lock()
	c.lastAction = time.Now()
	c.mu.Unlock()
}

// textChunk keeps one `type` line well inside a single BLE write.
const textChunk = 160

func (s *Server) io(ctx context.Context, c *Conn, req Request) Response {
	live := requireLive()
	coordinates := req.Cmd == "tap" || req.Cmd == "longpress" || req.Cmd == "swipe"
	ctx, done, err := c.beginOperation(ctx, live || coordinates)
	if err != nil {
		return fail(err)
	}
	defer done()
	var img image.Image
	if live || coordinates {
		if img, err = s.frame(ctx, c); err != nil {
			return fail(err)
		}
	}
	if coordinates {
		if err := validateCoordinates(req, img.Bounds()); err != nil {
			return fail(err)
		}
	}
	p := c.pointer(s.Dir)
	p.RequireLive = live
	err = func() error {
		switch req.Cmd {
		case "text":
			typed, err := "", error(nil)
			for rest := pointer.Typeable(req.Text); rest != "" && err == nil; {
				n := min(len(rest), textChunk)
				for n > 0 && !utf8.RuneStart(rest[n-1]) {
					n--
				}
				_, err = p.Type(ctx, rest[:n])
				typed, rest = typed+rest[:n], rest[n:]
			}
			if typed == "" && err == nil {
				err = errors.New("nothing typeable in that text (the board types ASCII only)")
			}
			return err
		case "keys":
			return p.Key(ctx, strings.ToLower(req.Text))
		case "button":
			if !strings.EqualFold(req.Text, "HOME") {
				return fmt.Errorf("button %s not supported on ESP32 Agent (only HOME)", req.Text)
			}
			return p.GoHome(ctx)
		}
		w, h := float64(img.Bounds().Dx()), float64(img.Bounds().Dy())
		if live && !p.CalibratedForLive() {
			if _, err := p.CalibrateVerified(ctx); err != nil {
				return err
			}
		} else if !p.Calibrated() {
			if err := p.Calibrate(ctx); err != nil {
				return err
			}
		}
		pt := func(i int) (float64, float64) { return req.Pts[i] / w, req.Pts[i+1] / h }
		switch req.Cmd {
		case "tap":
			x, y := pt(0)
			return p.Tap(ctx, x, y, req.Aim, req.Count)
		case "longpress":
			x, y := pt(0)
			d := time.Duration(req.Ms) * time.Millisecond
			if d <= 0 {
				d = time.Second
			}
			return p.LongPress(ctx, x, y, d)
		case "swipe":
			x1, y1 := pt(0)
			x2, y2 := pt(2)
			return p.Swipe(ctx, x1, y1, x2, y2, true)
		}
		return nil
	}()
	c.noteAction()
	s.recordAction(c, req, err)
	if err != nil {
		return fail(err)
	}
	out := map[string]string{"message": req.Cmd + " done"}
	if req.Cmd == "text" && pointer.HasHan(req.Text) {
		out["typed_as"] = pointer.Typeable(req.Text)
		out["note"] = "needs the iPhone's Chinese (pinyin) keyboard; if letters appear, tap the globe key and type again"
	}
	return ok(out)
}

func validateCoordinates(req Request, bounds image.Rectangle) error {
	expected := 2
	if req.Cmd == "swipe" {
		expected = 4
	}
	if len(req.Pts) != expected {
		return errors.New("wrong number of screen coordinates")
	}
	limits := [2]float64{float64(bounds.Dx()), float64(bounds.Dy())}
	for i, value := range req.Pts {
		if math.IsNaN(value) || value < 0 || value >= limits[i%2] {
			return errors.New("screen coordinates must be finite and within the current image")
		}
	}
	return nil
}

// screencapture streams raw H.264 (Annex-B, from the next keyframe) or MJPEG, after one JSON header line.
func (s *Server) screencapture(ctx context.Context, req Request, w io.Writer) {
	enc := json.NewEncoder(w)
	c, err := s.conn(ctx, req.Device)
	if err == nil {
		err = s.videoState(ctx, c)
	}
	if err == nil && req.Format != "h264" && req.Format != "mjpeg" {
		err = errors.New("format must be h264 or mjpeg")
	}
	if err != nil {
		enc.Encode(fail(err))
		return
	}
	enc.Encode(ok(map[string]string{"format": req.Format}))
	if req.Format == "h264" {
		ch := make(chan video.AU, 256)
		c.mu.Lock()
		c.subs[ch] = true
		config, gop := c.config, append([]video.AU{}, c.gop...)
		c.mu.Unlock()
		defer func() { c.mu.Lock(); delete(c.subs, ch); c.mu.Unlock() }()
		started := len(gop) > 0 && config != nil
		if started {
			w.Write(config)
			for _, au := range gop {
				w.Write(au.Data)
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case au := <-ch:
				if !started && au.Flags&(video.IDR|video.Config) == 0 {
					continue
				}
				if !started && au.Flags&video.Config == 0 && config != nil {
					w.Write(config)
				}
				started = true
				if _, err := w.Write(au.Data); err != nil {
					return
				}
			}
		}
	}
	ch := make(chan image.Image, 4)
	c.mu.Lock()
	c.frameSubs[ch] = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.frameSubs, ch); c.mu.Unlock() }()
	for {
		select {
		case <-ctx.Done():
			return
		case img := <-ch:
			data, _, _ := encode(img, "jpeg", req.Quality, 1, 0)
			hdr := fmt.Sprintf("--BoundaryString\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(data))
			if _, err := w.Write(append(append([]byte(hdr), data...), "\r\n"...)); err != nil {
				return
			}
		}
	}
}
