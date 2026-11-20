package video

import (
	"bytes"
	"context"
	"image"
	"os/exec"
	"testing"
	"time"
)

// The marker identifies the decoded picture independently of the decoder's
// FIFO timestamp. All media is synthetic and stays in memory.
const replayFixtureWidth, replayFixtureHeight, replayFixturePictures = 160, 96, 192

func fixtureMarker(img image.Image) int {
	var marker int
	for bit := range 8 {
		r, _, _, _ := img.At(8+bit*12, 8).RGBA()
		if r > 0x8000 {
			marker |= 1 << bit
		}
	}
	return marker
}

func replayFixtureAUs(t *testing.T, ffmpeg string) [][]byte {
	t.Helper()
	pixels := bytes.Repeat([]byte{16}, replayFixtureWidth*replayFixtureHeight*replayFixturePictures)
	for index := range replayFixturePictures {
		frame := pixels[index*replayFixtureWidth*replayFixtureHeight:]
		for bit := range 8 {
			if index&(1<<bit) == 0 {
				continue
			}
			for y := 4; y < 12; y++ {
				for x := 4 + bit*12; x < 12+bit*12; x++ {
					frame[y*replayFixtureWidth+x] = 235
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	encoder := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "rawvideo",
		"-pix_fmt", "gray", "-video_size", "160x96", "-framerate", "5", "-i", "pipe:0", "-pix_fmt", "yuv420p",
		"-c:v", "libx264", "-profile:v", "baseline", "-tune", "zerolatency", "-g", "192",
		"-x264-params", "aud=1:scenecut=0", "-f", "h264", "pipe:1")
	encoder.Stdin = bytes.NewReader(pixels)
	h264, err := encoder.Output()
	if err != nil {
		if failed, ok := err.(*exec.ExitError); ok {
			if bytes.Contains(failed.Stderr, []byte("Unknown encoder 'libx264'")) {
				t.Skip("ffmpeg has no libx264 encoder for the synthetic fixture")
			}
			t.Fatalf("generate low-bitrate H.264 fixture: %v\n%s", err, failed.Stderr)
		}
		t.Fatalf("generate low-bitrate H.264 fixture: %v", err)
	}
	if !bytes.Contains(h264, aud[:5]) {
		t.Fatal("fixture encoder did not emit delimiters")
	}
	aus := splitAUs(h264)
	if len(aus) != replayFixturePictures {
		t.Fatalf("generated %d AUs, want %d", len(aus), replayFixturePictures)
	}
	return aus
}

func TestDecoderReplayPreservesEveryPictureTimestamp(t *testing.T) {
	ffmpeg, err := FFmpeg()
	if err != nil {
		t.Skip(err)
	}
	aus := replayFixtureAUs(t, ffmpeg)
	fixtureTS := func(index int) uint32 { return uint32(1000 + 200*index) }
	for _, tc := range []struct {
		name   string
		filler int
		lead   int
	}{{"all picture AUs", 0, 0}, {"three non-VCL AUs presented as pictures", 3, 0},
		{"three P pictures after CONFIG but before IDR", 0, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			type observed struct {
				index int
				ts    uint32
			}
			var observations []observed
			d, err := NewDecoder(ffmpeg, func(frame Frame) {
				observations = append(observations, observed{fixtureMarker(frame.Img), frame.TS})
			})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			r := NewReassembler()
			var seq uint16
			reassemble := func(flags byte, timestamp uint32, data []byte) *AU {
				payload := append(ts(timestamp, ""), data...)
				au := r.Feed(chunk(seq, Start|End|flags, payload))
				seq++
				return au
			}
			if tc.lead > 0 {
				first := aus[0][6:]
				configEnd := -1
				for i := 1; i+4 < len(first); i++ {
					if first[i] == 0 && first[i+1] == 0 && first[i+2] == 1 && first[i+3]&0x1f == 5 {
						configEnd = i - 1
						break
					}
				}
				if configEnd <= 0 {
					t.Fatal("could not separate parameter sets and initial IDR")
				}
				config := reassemble(Config, 10, first[:configEnd])
				if config == nil || !d.Write(*config) {
					t.Fatal("decoder refused CONFIG")
				}
				for i := range tc.lead {
					if picture := reassemble(0, 100+uint32(i), aus[80+i][6:]); picture != nil {
						t.Fatal("pre-IDR P picture reached the decoder after CONFIG")
					}
				}
			}
			for i, au := range aus {
				if i == 40 {
					for n := tc.filler; n > 0; n-- {
						// Annex-B filler-data NAL. It can be consumed, but
						// cannot produce a decoded picture.
						filler := reassemble(0, fixtureTS(i)-uint32(n), []byte{0, 0, 0, 1, 12, 0xff, 0x80})
						if filler == nil || !d.Write(*filler) {
							t.Fatal("decoder refused fixture control AU")
						}
					}
				}
				var flags byte
				if i == 0 {
					flags = IDR
				}
				picture := reassemble(flags, fixtureTS(i), au[6:])
				if picture == nil || !d.Write(*picture) {
					t.Fatal("decoder refused fixture picture AU")
				}
				if i >= len(aus)-8 {
					time.Sleep(25 * time.Millisecond)
				}
			}
			time.Sleep(250 * time.Millisecond)
			beforeEOF := d.Stats()
			close(d.queue) // drain queued input and send EOF without killing ffmpeg
			select {
			case <-d.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("decoder did not drain after graceful EOF")
			}
			afterEOF := d.Stats()
			t.Logf("before EOF %+v; after EOF %+v", beforeEOF, afterEOF)
			if len(observations) != len(aus) || afterEOF.PicturesFed != uint64(len(aus)) ||
				afterEOF.BMPsRead != uint64(len(aus)) || afterEOF.PendingTS != 0 {
				t.Fatalf("unexpected complete-picture count %d or timestamp remainder: %+v; decoder: %v",
					len(observations), afterEOF, d.Err())
			}
			for i, frame := range observations {
				if frame.index != i || frame.ts != fixtureTS(i) {
					t.Fatalf("frame %d carries marker %d and time %d, want timestamp %d",
						i, frame.index, frame.ts, fixtureTS(i))
				}
			}
		})
	}
}
