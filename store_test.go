//go:build tinygo && esp32s3

package tinygo_esp32_flash

import (
	"bytes"
	"errors"
	"testing"
)

type memoryFlash struct {
	data      []byte
	failWrite bool
}

func fresh() *memoryFlash {
	f := &memoryFlash{data: make([]byte, 8192)}
	for i := range f.data {
		f.data[i] = 255
	}
	return f
}
func (f *memoryFlash) ReadAt(p []byte, off int64) (int, error) {
	return copy(p, f.data[int(off):]), nil
}
func (f *memoryFlash) WriteAt(p []byte, off int64) (int, error) {
	if f.failWrite {
		copy(f.data[int(off):], p[:len(p)/2])
		return 0, errors.New("power loss")
	}
	for i, b := range p {
		f.data[int(off)+i] &= b
	}
	return len(p), nil
}
func (f *memoryFlash) EraseBlocks(start, count int64) error {
	for i := start * 4096; i < (start+count)*4096; i++ {
		f.data[i] = 255
	}
	return nil
}
func (f *memoryFlash) EraseBlockSize() int64 { return 4096 }
func (f *memoryFlash) Size() int64           { return int64(len(f.data)) }
func TestRoundTripAndInterruptedSave(t *testing.T) {
	f := fresh()
	s := New(f)
	cfg := map[string]any{"name": "default"}
	if err := s.Load(&cfg); err != nil || cfg["name"] != "default" {
		t.Fatal(cfg, err)
	}
	if err := s.Save(map[string]string{"name": "first"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(f.data, []byte("{\n  \"name\": \"first\"\n}")) {
		t.Fatal("JSON is not human readable")
	}
	f.failWrite = true
	if err := s.Save(map[string]string{"name": "second"}); err == nil {
		t.Fatal("missing write error")
	}
	f.failWrite = false
	var got map[string]string
	if err := New(f).Load(&got); err != nil || got["name"] != "first" {
		t.Fatal(got, err)
	}
	if err := s.Save(map[string]string{"name": "third"}); err != nil {
		t.Fatal(err)
	}
	if err := New(f).Load(&got); err != nil || got["name"] != "third" {
		t.Fatal(got, err)
	}
	// Corrupt the most recent slot: recover the previous complete document.
	f.data[4096+24] ^= 1
	if err := New(f).Load(&got); err != nil || got["name"] != "first" {
		t.Fatal(got, err)
	}
	f.data[24] ^= 1
	if err := New(f).Load(&got); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}
func TestOversizeDoesNotErase(t *testing.T) {
	f := fresh()
	s := New(f)
	if err := s.Save("old"); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), f.data...)
	if err := s.Save(string(make([]byte, 5000))); err == nil {
		t.Fatal("oversize accepted")
	}
	if !bytes.Equal(before, f.data) {
		t.Fatal("changed flash")
	}
}
