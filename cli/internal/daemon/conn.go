package daemon

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"log"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/pointer"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/proto"
	"github.com/onehumanbeing/esp32-iphone-agent/cli/internal/video"
)

// Conn is one board: its current session (reconnected on demand), status, latest frame and pointer.
type Conn struct {
	ID   string // board id, 12 hex
	Name string

	mu            sync.Mutex
	swap          sync.Mutex    // serializes wire writes and session replacement
	operation     chan struct{} // at most one pointer operation per board
	sess          *proto.Session
	via           string        // the session's link: "ble" or "wifi"
	quit          chan struct{} // stops the session's reader (a BLE session moved to Wi-Fi keeps its link)
	bleLink       proto.Link    // the BLE link, kept while the session runs over Wi-Fi: the way back
	links         int
	nextID        int
	pending       map[string]chan string
	status        map[string]any
	statusAt      time.Time
	videoStatusAt time.Time // when the board last provided both AirPlay and freeze state
	hidStatusAt   time.Time // when one report last carried both input mode and connection state

	re               *video.Reassembler
	videoGen         int // changes when old decoder output must no longer be published
	restartingStream bool
	streamPaused     bool
	waitingForReplay bool
	restartAfter     time.Time // successful stream-on acknowledgment in the current session
	videoCounts      videoCounts
	dec              *video.Decoder
	ffmpeg           string
	frame            image.Image
	frameHash        uint64
	captured         time.Time  // when the board got the shown frame (local clock)
	updated          time.Time  // when it was decoded here
	changed          time.Time  // when the shown image last differed from the one before
	offset           *int64     // local ms − board ms, the smallest seen (≈ the link latency)
	config           []byte     // last SPS/PPS, for screencapture
	gop              []video.AU // AUs since the last IDR, so a new screencapture starts at once
	subs             map[chan video.AU]bool
	frameSubs        map[chan image.Image]bool
	wake             chan struct{} // closed and replaced on every new frame

	onApp      func() // the board said the app is using it
	ptr        *pointer.Pointer
	lastAction time.Time
	lastShot   time.Time

	keyMu   sync.Mutex // held across the gap and the send of key/type commands
	lastKey time.Time  // when the board acknowledged the last one

	wifi *wifiLink // moves the session to the board's Wi-Fi and back (wifi.go); nil: BLE only

	hear      *hearing    // phone audio transcribed here (listen.go); nil: never
	rawWanted func() bool // ask each session for the raw audio
}

// KeyGap separates key and type commands: sent back to back, iOS dropped a backspace (2026-10-21).
const KeyGap = 80 * time.Millisecond

func newConn(id, name, ffmpeg string) *Conn {
	return &Conn{ID: id, Name: name, ffmpeg: ffmpeg, pending: map[string]chan string{}, status: map[string]any{},
		subs: map[chan video.AU]bool{}, frameSubs: map[chan image.Image]bool{}, wake: make(chan struct{}),
		operation: make(chan struct{}, 1)}
}

func (c *Conn) connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess != nil
}

// attach takes over a fresh session on link l (via "ble" or "wifi") and turns on the CLI's own video stream.
// The session it replaces stops: a BLE one keeps its link as the way back, a Wi-Fi one is closed.
func (c *Conn) attach(s *proto.Session, via string, l proto.Link) error {
	c.swap.Lock()
	defer c.swap.Unlock()
	c.mu.Lock()
	old, oldVia := c.sess, c.via
	if c.quit != nil {
		close(c.quit)
	}
	quit := make(chan struct{})
	c.sess, c.via, c.quit, c.links = s, via, quit, c.links+1
	if via == "ble" {
		if c.bleLink != nil && c.bleLink != l {
			c.bleLink.Close()
		}
		c.bleLink = l
	}
	c.resetSessionVideo()
	c.mu.Unlock() // pending commands stay: the board sends their replies on the new session
	if old != nil && oldVia == "wifi" {
		old.Close()
	}
	go c.read(s, quit)
	return nil
}

// start turns on the CLI's video stream on a fresh session; also the client's proof of K (the board sends
// nothing before it), so the board moves the session to this link now.
func (c *Conn) start() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.Send(ctx, "stream on")
	if err == nil && c.rawWanted != nil && c.rawWanted() && c.Via() == "wifi" {
		c.Send(ctx, "listen raw on") // the board forgets it with every session
	}
	return err
}

func (c *Conn) detach(s *proto.Session, why error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != s {
		return
	}
	s.Close()
	if c.via == "ble" {
		c.bleLink = nil
	} else if c.wifi != nil && c.bleLink != nil {
		go c.wifi.fallBack(c, c.bleLink) // after mu is released
	}
	c.sess = nil
	c.resetSessionVideo()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	log.Printf("%s: session ended: %v", c.ID, why)
}

// resetSessionVideo must hold mu. The new session needs its own status and board clock.
func (c *Conn) resetSessionVideo() {
	c.status, c.statusAt, c.videoStatusAt, c.offset = map[string]any{}, time.Time{}, time.Time{}, nil
	c.hidStatusAt = time.Time{}
	c.restartingStream, c.streamPaused, c.waitingForReplay = false, false, false
	c.restartAfter = time.Time{}
	c.resetVideo()
}

// resetVideo must hold mu.
func (c *Conn) resetVideo() {
	c.videoGen++
	c.re = video.NewReassembler()
	c.clearFrame()
	c.gop, c.config = nil, nil
	if c.dec != nil {
		c.dec.Close()
		c.dec = nil
	}
	c.signalVideo()
}

func (c *Conn) clearFrame() {
	c.frame, c.captured, c.updated, c.changed = nil, time.Time{}, time.Time{}, time.Time{}
	c.frameHash, c.lastShot = 0, time.Time{}
	c.videoCounts = videoCounts{}
	if c.re != nil {
		c.videoCounts.baseGaps = c.re.Gaps
	}
}

func (c *Conn) signalVideo() {
	close(c.wake)
	c.wake = make(chan struct{})
}

func (c *Conn) close() {
	if c.wifi != nil {
		c.wifi.stop()
	}
	c.mu.Lock()
	s, bl := c.sess, c.bleLink
	c.bleLink = nil
	c.mu.Unlock()
	if s != nil {
		c.detach(s, errors.New("closed"))
	}
	if bl != nil {
		bl.Close()
	}
}

// Via is the current session's link ("ble", "wifi"), "" without one.
func (c *Conn) Via() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return ""
	}
	return c.via
}

func (c *Conn) read(s *proto.Session, quit chan struct{}) {
	for {
		var f []byte
		var open bool
		select {
		case <-quit:
			return
		case f, open = <-s.Recv():
		}
		if !open {
			break
		}
		m, ok, err := s.Decode(f)
		if err != nil {
			c.detach(s, err)
			return
		}
		if !ok {
			continue
		}
		c.mu.Lock()
		currentSession := c.sess == s
		c.mu.Unlock()
		if !currentSession {
			return
		}
		switch m.Kind {
		case 'r':
			id, rest, _ := strings.Cut(string(m.Data), " ")
			c.mu.Lock()
			if ch := c.pending[id]; ch != nil {
				ch <- rest
				delete(c.pending, id)
			}
			c.mu.Unlock()
		case 's':
			c.onStatus(m.Data, s)
		case 'v':
			c.onVideo(m.Data, m.Gap, s)
		case 'a':
			c.onAudio(m.Data, m.Gap)
		}
	}
	c.detach(s, proto.ErrSession)
}

type operationKey struct{}

type operationBinding struct {
	session    *proto.Session
	generation int
	live       bool
	refresh    bool
}

func (c *Conn) beginOperation(ctx context.Context, live bool) (context.Context, func(), error) {
	c.mu.Lock()
	binding := operationBinding{session: c.sess, generation: c.videoGen, live: live}
	c.mu.Unlock()
	if binding.session == nil {
		return nil, nil, proto.ErrSession
	}
	select {
	case c.operation <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	done := func() { <-c.operation }
	if err := ctx.Err(); err != nil {
		done()
		return nil, nil, err
	}
	return context.WithValue(ctx, operationKey{}, binding), done, nil
}

// operationError must hold mu. A release can clean up a prior press on its
// original session after video is lost or the request is canceled.
func (c *Conn) operationError(ctx context.Context, line string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.sess == nil {
		return proto.ErrSession
	}
	if line == "snap" && c.streamRestartOwnsReplay() {
		return errStreamRestarting
	}
	binding, bound := ctx.Value(operationKey{}).(operationBinding)
	if !bound {
		return nil
	}
	if binding.session != c.sess {
		return errVideoChanged
	}
	if line == "release" && !binding.refresh {
		return nil
	}
	if binding.generation != c.videoGen {
		return errVideoChanged
	}
	if binding.refresh {
		if line != "move 1 0" && line != "move -1 0" {
			return errors.New("screen refresh can only move the pointer by one unit")
		}
		now := time.Now()
		if err := c.videoState(now); err != nil {
			return err
		}
		hid := c.status["hid"]
		if !currentVideoTime(now, c.hidStatusAt) || c.status["hidm"] != "ble" ||
			(hid != "ok" && hid != "on" && hid != "connected") {
			return errRefreshHID
		}
	}
	if binding.live {
		now := time.Now()
		if err := c.videoState(now); err != nil {
			return err
		}
		if line != "snap" && (c.frame == nil || !currentVideoTime(now, c.captured)) {
			return errFrameLag
		}
	}
	return nil
}

// Send runs one command and returns the reply after "ok"; an "err" reply is an error.
func (c *Conn) Send(ctx context.Context, line string) (string, error) {
	c.swap.Lock()
	c.mu.Lock()
	if err := c.operationError(ctx, line); err != nil {
		c.mu.Unlock()
		c.swap.Unlock()
		return "", err
	}
	s := c.sess
	c.nextID++
	id := strconv.Itoa(c.nextID)
	ch := make(chan string, 1)
	c.pending[id] = ch
	err := s.Send(id + " " + line)
	c.mu.Unlock()
	c.swap.Unlock()
	if err != nil {
		c.detach(s, err)
		return "", err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return "", fmt.Errorf("%s: no reply from the board", strings.Fields(line)[0])
	case r, ok := <-ch:
		if !ok {
			return "", proto.ErrSession
		}
		if r == "ok" || strings.HasPrefix(r, "ok ") {
			return strings.TrimPrefix(strings.TrimPrefix(r, "ok"), " "), nil
		}
		if strings.HasPrefix(r, "err busy: the app") {
			c.appBusy()
			return "", ErrAppBusy
		}
		return "", errors.New(strings.TrimPrefix(r, "err "))
	}
}

func (c *Conn) appBusy() {
	if c.onApp != nil {
		c.onApp()
	}
}

// onStatus merges a status notify; the compact one only carries a few keys.
func (c *Conn) onStatus(b []byte, expected ...*proto.Session) {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return
	}
	now := time.Now()
	c.mu.Lock()
	if len(expected) != 0 && c.sess != expected[0] {
		c.mu.Unlock()
		return
	}
	if up, ok := m["up"].(float64); ok {
		if previous, known := c.status["up"].(float64); known && up+1000 < previous {
			c.resetSessionVideo() // the same board rebooted; its clock origin changed
		}
	}
	wasStreaming := streamActive(c.status)
	notify := false
	defer func() {
		c.mu.Unlock()
		if notify {
			c.appBusy()
		}
	}()
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			if old, ok := c.status[k].(map[string]any); ok {
				for sk, sv := range sub {
					old[sk] = sv
				}
				continue
			}
		}
		c.status[k] = v
	}
	c.statusAt = now
	_, hasHID := m["hid"]
	_, hasHIDMode := m["hidm"]
	if hasHID || hasHIDMode {
		c.hidStatusAt = time.Time{}
		if hasHID && hasHIDMode {
			c.hidStatusAt = now
		}
	}
	if _, hasAirPlay := m["ap"].(string); hasAirPlay {
		if _, hasFreeze := m["fz"].(float64); hasFreeze {
			c.videoStatusAt = now
		}
	}
	notify = m["owner"] == "app" || m["busy"] == float64(1)
	if up, ok := m["up"].(float64); ok {
		c.learnClock(now, uint32(up))
	}
	if wasStreaming && !streamActive(c.status) {
		c.resetVideo()
	} else {
		c.signalVideo()
	}
}

// learnClock must hold mu.
func (c *Conn) learnClock(now time.Time, boardMs uint32) {
	off := now.UnixMilli() - int64(boardMs)
	if c.offset == nil || off < *c.offset {
		c.offset = &off
	}
}

func (c *Conn) onVideo(chunk []byte, gap bool, expected ...*proto.Session) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if (c.streamPaused && !c.waitingForReplay) || (len(expected) != 0 && c.sess != expected[0]) {
		return
	}
	if gap {
		c.re.Lost()
	}
	if len(chunk) >= 7 && chunk[2]&video.Start != 0 {
		c.learnClock(time.Now(), binary.LittleEndian.Uint32(chunk[3:]))
	}
	au := c.re.Feed(chunk)
	if au == nil {
		return
	}
	if au.Flags&(video.IDR|video.Config) != video.Config {
		c.videoCounts.pictures++
		c.videoCounts.latestAU = au.TS
	}
	switch {
	case au.Flags&video.Config != 0:
		c.config = au.Data
	case au.Flags&video.IDR != 0:
		c.gop = []video.AU{*au}
	case c.gop != nil && len(c.gop) < 600:
		c.gop = append(c.gop, *au)
	}
	for ch := range c.subs {
		select {
		case ch <- *au:
		default:
		}
	}
	if c.dec == nil && c.ffmpeg != "" && au.Flags&(video.Config|video.IDR) != 0 {
		generation := c.videoGen
		d, err := video.NewDecoder(c.ffmpeg, func(f video.Frame) { c.onFrame(generation, f) },
			func(ts uint32) bool { return c.recentVideoTimestamp(generation, ts) })
		if err != nil {
			log.Printf("%s: decoder: %v", c.ID, err)
			c.ffmpeg = ""
			return
		}
		c.dec = d
		if au.Flags&video.Config == 0 && c.config != nil {
			d.Write(video.AU{Flags: video.Config, Data: c.config})
		}
	}
	if c.dec != nil && !c.dec.Write(*au) {
		log.Printf("%s: decoder fell behind or stopped (%v); restarting at the next IDR", c.ID, c.dec.Err())
		c.dec.Close()
		c.dec = nil
		c.videoGen++
		c.clearFrame()
		c.signalVideo()
		c.re.Lost()
	}
}

// Replay old reference pictures in ffmpeg, but avoid expanding/hash-publishing
// thousands of old BMPs while reconnecting to a long cached GOP.
func (c *Conn) recentVideoTimestamp(generation int, ts uint32) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.captureTime(generation, ts, time.Now()).IsZero()
}

const maxVideoAge = 2 * time.Second

func streamActive(status map[string]any) bool {
	return status["ap"] == "streaming" && status["fz"] == float64(0)
}

type videoCounts struct {
	pictures  uint64
	latestAU  uint32
	published uint64
	rejected  map[string]uint64
	baseGaps  int
}

// captureTime must hold mu. An unknown or future timestamp cannot certify a live frame.
func (c *Conn) captureTime(generation int, ts uint32, now time.Time) time.Time {
	switch {
	case generation != c.videoGen:
		return c.rejectFrame("generation")
	case ts == 0 || c.offset == nil:
		return c.rejectFrame("clock")
	case c.videoState(now) != nil:
		return c.rejectFrame("status")
	}
	if _, ok := c.status["up"]; !ok {
		return c.rejectFrame("clock")
	}
	captured := time.UnixMilli(int64(ts) + *c.offset)
	switch {
	case captured.After(now):
		return c.rejectFrame("future")
	case !currentVideoTime(now, captured):
		return c.rejectFrame("old")
	case !captured.After(c.restartAfter):
		return c.rejectFrame("pre_restart")
	}
	return captured
}

func (c *Conn) rejectFrame(reason string) time.Time {
	if c.videoCounts.rejected == nil {
		c.videoCounts.rejected = make(map[string]uint64)
	}
	c.videoCounts.rejected[reason]++
	return time.Time{}
}

func currentVideoTime(now, at time.Time) bool {
	age := now.Sub(at)
	return age >= 0 && age <= maxVideoAge
}

func hashImage(img image.Image) uint64 {
	h := fnv.New64a()
	if r, ok := img.(*image.RGBA); ok {
		h.Write(r.Pix)
	}
	return h.Sum64()
}

func (c *Conn) onFrame(generation int, f video.Frame) {
	hash := hashImage(f.Img)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	captured := c.captureTime(generation, f.TS, now)
	if captured.IsZero() {
		return
	}
	c.updated, c.captured = now, captured
	if hash != c.frameHash || c.frame == nil {
		c.changed = now
	}
	c.frame, c.frameHash = f.Img, hash
	c.waitingForReplay = false
	c.videoCounts.published++
	for ch := range c.frameSubs {
		select {
		case ch <- f.Img:
		default:
		}
	}
	c.signalVideo()
}

// videoDiagnostics reports numeric decoder progress without retaining any frame content.
func (c *Conn) videoDiagnostics() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	counts := c.videoCounts
	out := map[string]any{
		"generation": c.videoGen, "complete_picture_aus": counts.pictures,
		"last_picture_au_ts_ms": counts.latestAU, "published_frames": counts.published,
		"rejected_frames": maps.Clone(counts.rejected),
	}
	if c.re != nil {
		out["reassembly_gaps"] = c.re.Gaps - counts.baseGaps
	}
	if !c.captured.IsZero() {
		out["last_published_age_ms"] = now.Sub(c.captured).Milliseconds()
	}
	if c.offset != nil && counts.latestAU != 0 {
		out["last_picture_au_age_ms"] = now.Sub(time.UnixMilli(int64(counts.latestAU) + *c.offset)).Milliseconds()
	}
	if c.dec != nil {
		stats := c.dec.Stats()
		out["decoder"] = stats
		if c.offset != nil && stats.LastBMPTime != 0 {
			out["decoder_assigned_output_age_ms"] = now.Sub(time.UnixMilli(int64(stats.LastBMPTime) + *c.offset)).Milliseconds()
		}
	}
	return out
}

// Status returns a copy of the merged status and its age.
func (c *Conn) Status() (map[string]any, time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]any{}
	for k, v := range c.status {
		if sub, ok := v.(map[string]any); ok { // copied: onStatus merges into it
			cp := map[string]any{}
			for sk, sv := range sub {
				cp[sk] = sv
			}
			v = cp
		}
		out[k] = v
	}
	if c.statusAt.IsZero() {
		return out, -1
	}
	return out, time.Since(c.statusAt)
}

// Screen (pointer.Screen).

func (c *Conn) Live() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	return c.videoState(now) == nil && c.frame != nil && currentVideoTime(now, c.captured)
}

func (c *Conn) Frame() image.Image {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frame
}

func (c *Conn) times() (captured, updated, changed time.Time, wake chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.captured, c.updated, c.changed, c.wake
}

// Settled waits until the screen stops changing. AirPlay keeps sending frames of a still screen, so this
// watches when the image last changed; a gap is only "quiet" once it also covers the last frame's lag.
func (c *Conn) Settled(ctx context.Context, quiet, least, most time.Duration) {
	start := time.Now()
	if sleepCtx(ctx, least) != nil {
		return
	}
	for time.Since(start) < most {
		captured, updated, changed, _ := c.times()
		lag := min(max(updated.Sub(captured), 0), time.Second)
		if time.Since(changed) >= quiet+lag {
			return
		}
		if sleepCtx(ctx, 100*time.Millisecond) != nil {
			return
		}
	}
}

// Fresh returns the first live frame captured after since, nil if none in time.
func (c *Conn) Fresh(ctx context.Context, since time.Time, timeout time.Duration) image.Image {
	c.mu.Lock()
	generation := c.videoGen
	c.mu.Unlock()
	if binding, ok := ctx.Value(operationKey{}).(operationBinding); ok {
		generation = binding.generation
	}
	frame, _ := c.waitFrame(ctx, generation, since, timeout, false)
	return frame
}

func (c *Conn) waitFrame(ctx context.Context, generation int, since time.Time, timeout time.Duration, snapshot bool) (image.Image, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		c.mu.Lock()
		now := time.Now()
		err := c.videoState(now)
		if err == nil && generation != c.videoGen {
			err = errVideoChanged
		}
		if err == nil {
			err = ctx.Err()
		}
		// Read the image and its capture time from the same generation, and check
		// the latest action and streaming state before accepting either.
		if err == nil && c.frame != nil && currentVideoTime(now, c.captured) && c.captured.After(since) &&
			(!snapshot || c.captured.After(c.lastAction)) {
			frame := c.frame
			if snapshot {
				c.lastShot = c.captured
			}
			c.mu.Unlock()
			return frame, nil
		}
		wake := c.wake
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errFrameLag
		case <-wake:
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Device (pointer.Device).

func (c *Conn) Links() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.links
}

type device struct{ c *Conn }

func (d device) Send(ctx context.Context, line string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if strings.HasPrefix(line, "key ") || strings.HasPrefix(line, "type ") {
		d.c.keyMu.Lock()
		defer d.c.keyMu.Unlock()
		if err := sleepCtx(ctx, time.Until(d.c.lastKey.Add(KeyGap))); err != nil {
			return err
		}
		defer func() { d.c.lastKey = time.Now() }()
	}
	_, err := d.c.Send(ctx, line)
	return err
}

func (d device) Links() int { return d.c.Links() }

func (d device) CheckOperation(ctx context.Context) error {
	d.c.mu.Lock()
	defer d.c.mu.Unlock()
	return d.c.operationError(ctx, "")
}

func idHex(b []byte) string { return hex.EncodeToString(b) }
