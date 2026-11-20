package pointer

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

func TestFixConsistentLargeBLEOriginOffset(t *testing.T) {
	p := newPointer(t, &fakeDevice{}, nil)
	p.remember("x", [2]float64{0.27, 0.415})
	p.remember("x", [2]float64{0.63, 0.775})
	if got := p.Fix(0.5, "x"); math.Abs(got-0.355) > 0.001 {
		t.Fatalf("consistent BLE origin should be corrected: got %v", got)
	}
	p.Span = &Span{W: 550, H: 1200}
	if got := p.dx(0.05); got >= 0 {
		t.Fatalf("left-edge origin correction was clamped away: %v", got)
	}
}

type unobservedPointerScreen struct{ img image.Image }

func (s *unobservedPointerScreen) Live() bool         { return true }
func (s *unobservedPointerScreen) Frame() image.Image { return s.img }
func (s *unobservedPointerScreen) Fresh(context.Context, time.Time, time.Duration) image.Image {
	return s.img
}
func (s *unobservedPointerScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {
}

func TestAimedTapDoesNotClickWhenPointerIsNotObserved(t *testing.T) {
	d := &fakeDevice{}
	s := &unobservedPointerScreen{img: image.NewRGBA(image.Rect(0, 0, 200, 400))}
	p := newPointer(t, d, s)
	if err := p.FinishCalibration(0.5, 0.25); err != nil {
		t.Fatal(err)
	}
	if err := p.Tap(context.Background(), 0.5, 0.5, true, 1); err == nil {
		t.Fatal("unobserved pointer accepted")
	}
	for _, command := range d.sent {
		if command == "click" {
			t.Fatal("click sent without observed pointer")
		}
	}
}

type changingLinkDevice struct {
	fakeDevice
	links int
}

func (d *changingLinkDevice) Links() int { return d.links }

type changingLinkScreen struct {
	unobservedPointerScreen
	device *changingLinkDevice
}

func (s *changingLinkScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {
	s.device.links++
}

func TestTapDoesNotClickAfterConnectionChanges(t *testing.T) {
	d := &changingLinkDevice{links: 1}
	s := &changingLinkScreen{unobservedPointerScreen: unobservedPointerScreen{img: image.NewRGBA(image.Rect(0, 0, 200, 400))}, device: d}
	p := newPointer(t, d, s)
	if err := p.FinishCalibration(0.5, 0.25); err != nil {
		t.Fatal(err)
	}
	if err := p.Tap(context.Background(), 0.5, 0.5, false, 1); err == nil {
		t.Fatal("replaced connection accepted")
	}
	for _, command := range d.sent {
		if command == "click" {
			t.Fatal("click sent after connection changed")
		}
	}
}

type settlingScreen struct {
	fakeScreen
	settled bool
}

func (s *settlingScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {
	s.settled = true
}

type settlingDevice struct {
	fakeDevice
	screen *settlingScreen
}

func (d *settlingDevice) Send(ctx context.Context, command string) error {
	if command == "click" && !d.screen.settled {
		return fmt.Errorf("clicked before pointer animation settled")
	}
	return d.fakeDevice.Send(ctx, command)
}

func TestTapWaitsForFinalPointerMovement(t *testing.T) {
	d := &settlingDevice{}
	s := &settlingScreen{fakeScreen: fakeScreen{d: &d.fakeDevice}}
	d.screen = s
	p := newPointer(t, d, s)
	if err := p.FinishCalibration(0.5, 0.25); err != nil {
		t.Fatal(err)
	}
	if err := p.Tap(context.Background(), 0.5, 0.5, false, 1); err != nil {
		t.Fatal(err)
	}
	if !s.settled {
		t.Fatal("pointer did not settle before click")
	}
}

func TestFixRejectsUncertainLargeOffset(t *testing.T) {
	p := newPointer(t, &fakeDevice{}, nil)
	p.remember("x", [2]float64{0.4, 0.545})
	p.remember("x", [2]float64{0.41, 0.555})
	if got := p.Fix(0.5, "x"); got != 0.5 {
		t.Fatalf("nearby samples do not establish a reliable origin: %v", got)
	}
	p = newPointer(t, &fakeDevice{}, nil)
	for _, v := range [][2]float64{{0.2, 0.345}, {0.5, 0.72}, {0.8, 0.945}} {
		p.remember("x", v)
	}
	if got := p.Fix(0.5, "x"); got != 0.5 {
		t.Fatalf("inconsistent samples accepted: %v", got)
	}
}

type preActuationScreen struct {
	fakeScreen
	onSettle func()
}

func (s *preActuationScreen) Settled(_ context.Context, _, least, _ time.Duration) {
	if least == 800*time.Millisecond {
		s.onSettle()
	}
}

func TestLiveGesturesAbortBeforeActuationWhenCanceledOrReconnected(t *testing.T) {
	gestures := map[string]func(*Pointer, context.Context) error{
		"tap":       func(p *Pointer, ctx context.Context) error { return p.Tap(ctx, 0.5, 0.5, false, 1) },
		"longpress": func(p *Pointer, ctx context.Context) error { return p.LongPress(ctx, 0.5, 0.5, time.Second) },
		"swipe":     func(p *Pointer, ctx context.Context) error { return p.Swipe(ctx, 0.5, 0.5, 0.5, 0.8, true) },
	}
	for gesture, run := range gestures {
		t.Run(gesture, func(t *testing.T) {
			for _, changeLink := range []bool{false, true} {
				ctx, cancel := context.WithCancel(context.Background())
				d := &changingLinkDevice{links: 1}
				hooked := false
				s := &preActuationScreen{fakeScreen: fakeScreen{d: &d.fakeDevice}}
				s.onSettle = func() {
					hooked = true
					if changeLink {
						d.links++
					} else {
						cancel()
					}
				}
				p := newPointer(t, d, s)
				p.RequireLive = true
				if err := p.FinishCalibration(0.5, 200.0/860); err != nil {
					t.Fatal(err)
				}
				err := run(p, ctx)
				cancel()
				if !hooked || err == nil || slices.Contains(d.sent, "click") || slices.Contains(d.sent, "press") {
					t.Fatalf("linkChange=%t reached actuation: hook=%t error=%v commands=%q", changeLink, hooked, err, d.sent)
				}
			}
		})
	}
}

type verificationScreen struct {
	d    *fakeDevice
	hide bool
}

func (s verificationScreen) Live() bool { return true }

func (s verificationScreen) Frame() image.Image {
	s.d.mu.Lock()
	defer s.d.mu.Unlock()
	homes := 0
	for _, line := range s.d.sent {
		if line == "home" {
			homes++
		}
	}
	shift := 0.0
	if homes > 1 {
		if s.hide {
			return frame(-100, -100)
		}
		shift = 0.08
	}
	return frame((0.01+float64(s.d.x)/410+shift)*W, (0.005+float64(s.d.y)/870)*H)
}

func (s verificationScreen) Fresh(context.Context, time.Time, time.Duration) image.Image {
	return s.Frame()
}
func (s verificationScreen) Settled(context.Context, time.Duration, time.Duration, time.Duration) {
}

func TestCalibrateVerifiedRejectsMissingAndInaccurateObservation(t *testing.T) {
	for _, hide := range []bool{true, false} {
		d := &fakeDevice{}
		p := newPointer(t, d, verificationScreen{d: d, hide: hide})
		p.RequireLive = true
		if err := p.FinishCalibration(0.5, 0.25); err != nil {
			t.Fatal(err)
		}
		prior, err := os.ReadFile(p.file)
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.CalibrateVerified(context.Background())
		if err == nil || p.CalibratedForLive() || *p.Span != (Span{400, 800}) {
			t.Fatalf("unverified calibration retained (hide=%t): %v, %+v", hide, err, p.Span)
		}
		after, err := os.ReadFile(p.file)
		if err != nil || string(after) != string(prior) {
			t.Fatalf("unverified calibration was saved (hide=%t): %v", hide, err)
		}
	}
}
