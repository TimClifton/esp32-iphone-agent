package video

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"testing"
)

type discardInput struct{}

func (discardInput) Write(p []byte) (int, error) { return len(p), nil }
func (discardInput) Close() error                { return nil }

func TestCachedGOPKeepsOriginalFrameTimes(t *testing.T) {
	d := &Decoder{in: discardInput{}, queue: make(chan AU, 80)}
	for i := 0; i < 80; i++ {
		d.queue <- AU{TS: uint32(100 + i), Data: []byte{0, 0, 1, 1, 0x80}}
	}
	close(d.queue)
	d.feed()
	stats := d.Stats()
	if stats.PicturesFed != 80 || stats.PendingTS != 80 || stats.LastInputTS != 179 || stats.BMPsRead != 0 {
		t.Fatalf("missing or inconsistent replay counters: %+v", stats)
	}
	for i := 0; i < 80; i++ {
		if got := d.popTS(); got != uint32(100+i) {
			t.Fatalf("cached picture %d was relabeled as time %d", i, got)
		}
	}
}

func TestDiscardOldBMPKeepsNextPictureAligned(t *testing.T) {
	picture := make([]byte, 54)
	copy(picture, "BM")
	binary.LittleEndian.PutUint32(picture[2:], 54)
	r := bufio.NewReader(bytes.NewReader(append(append([]byte{}, picture...), picture...)))
	if got, err := readBMP(r, func() bool { return false }); err != nil || got != nil {
		t.Fatalf("old picture not discarded: %v", err)
	}
	if got, err := readBMP(r); err != nil || len(got) != 54 {
		t.Fatalf("next picture damaged: %v", err)
	}
}
