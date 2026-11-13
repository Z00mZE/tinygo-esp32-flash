//go:build tinygo && esp32s3

package tinygo_esp32_flash

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"sync"
)

const sectorSize = 4096
const headerSize = 24
const magic uint32 = 0x53474643

var ErrCorrupt = errors.New("config: both flash copies invalid")

// Flash is independent of machine and of the application's configuration schema.
type Flash interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	EraseBlocks(int64, int64) error
	EraseBlockSize() int64
	Size() int64
}
type Store struct {
	flash Flash
	mu    sync.Mutex
}

func New(f Flash) *Store { return &Store{flash: f} }
func (s *Store) check() error {
	if s.flash.EraseBlockSize() != sectorSize || s.flash.Size() != 2*sectorSize {
		return errors.New("config: use target targets/lilygo-t-display-s3.json with reserved 8 KiB flash")
	}
	return nil
}
func (s *Store) read(slot int) ([]byte, uint64, bool, error) {
	b := make([]byte, sectorSize)
	n, err := s.flash.ReadAt(b, int64(slot*sectorSize))
	if err != nil {
		return nil, 0, false, err
	}
	if n != len(b) {
		return nil, 0, false, io.ErrUnexpectedEOF
	}
	erased := true
	for _, v := range b {
		if v != 255 {
			erased = false
			break
		}
	}
	if binary.LittleEndian.Uint32(b) != magic || binary.LittleEndian.Uint32(b[4:]) != 1 {
		return nil, 0, erased, nil
	}
	length := int(binary.LittleEndian.Uint32(b[16:]))
	if length <= 0 || length > sectorSize-headerSize {
		return nil, 0, false, nil
	}
	crc := crc32.NewIEEE()
	crc.Write(b[4:20])
	crc.Write(b[headerSize : headerSize+length])
	if crc.Sum32() != binary.LittleEndian.Uint32(b[20:]) || !json.Valid(b[headerSize:headerSize+length]) {
		return nil, 0, false, nil
	}
	return b[headerSize : headerSize+length], binary.LittleEndian.Uint64(b[8:]), false, nil
}

// Load decodes JSON into dst. Empty flash leaves dst's defaults unchanged.
func (s *Store) Load(dst any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	a, ga, ea, err := s.read(0)
	if err != nil {
		return err
	}
	b, gb, eb, err := s.read(1)
	if err != nil {
		return err
	}
	if b != nil && (a == nil || gb > ga) {
		a = b
	}
	if a != nil {
		return json.Unmarshal(a, dst)
	}
	if ea && eb {
		return nil
	}
	return ErrCorrupt
}

// Save stores readable JSON. The previous sector stays intact until commit.
func (s *Store) Save(value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(payload) > sectorSize-headerSize {
		return errors.New("config JSON exceeds 4072 bytes")
	}
	a, ga, _, err := s.read(0)
	if err != nil {
		return err
	}
	b, gb, _, err := s.read(1)
	if err != nil {
		return err
	}
	slot := 0
	gen := uint64(1)
	if a != nil && (b == nil || ga >= gb) {
		slot = 1
		gen = ga + 1
	} else if b != nil {
		gen = gb + 1
	}
	if gen == 0 {
		return errors.New("config generation overflow")
	}
	record := make([]byte, (headerSize+len(payload)+3)&^3)
	for i := range record {
		record[i] = 255
	}
	binary.LittleEndian.PutUint32(record[4:], 1)
	binary.LittleEndian.PutUint64(record[8:], gen)
	binary.LittleEndian.PutUint32(record[16:], uint32(len(payload)))
	copy(record[headerSize:], payload)
	crc := crc32.NewIEEE()
	crc.Write(record[4:20])
	crc.Write(payload)
	binary.LittleEndian.PutUint32(record[20:], crc.Sum32())
	if err = s.flash.EraseBlocks(int64(slot), 1); err != nil {
		return err
	}
	off := int64(slot * sectorSize)
	if n, e := s.flash.WriteAt(record[4:], off+4); e != nil {
		return e
	} else if n != len(record)-4 {
		return io.ErrShortWrite
	}
	binary.LittleEndian.PutUint32(record, magic)
	if n, e := s.flash.WriteAt(record[:4], off); e != nil {
		return e
	} else if n != 4 {
		return io.ErrShortWrite
	}
	got, g, _, err := s.read(slot)
	if err != nil {
		return err
	}
	if got == nil || g != gen {
		return errors.New("config: flash verification failed")
	}
	return nil
}
