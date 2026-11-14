// Package dovirpu converts Dolby Vision Profile 7 RPUs to Profile 8.1 and
// rewrites them inside fragmented MP4 segments.
//
// The RPU conversion is a port of dovi_tool's mode 2 (MIT License,
// Copyright (c) quietvoid, https://github.com/quietvoid/dovi_tool; see
// testdata/LICENSE-dovi_tool).
package dovirpu

import "errors"

var errShort = errors.New("dovirpu: RPU ends early")

type bitReader struct {
	data []byte
	pos  int // bit offset
}

func (r *bitReader) bits(n int) (uint64, error) {
	if n < 0 || n > 64 || r.pos+n > len(r.data)*8 {
		return 0, errShort
	}
	var v uint64
	for range n {
		v = v<<1 | uint64(r.data[r.pos>>3]>>(7-r.pos&7)&1)
		r.pos++
	}
	return v, nil
}

func (r *bitReader) skip(n int) error {
	if r.pos+n > len(r.data)*8 {
		return errShort
	}
	r.pos += n
	return nil
}

func (r *bitReader) ue() (uint64, error) {
	zeros := 0
	for {
		b, err := r.bits(1)
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		if zeros++; zeros > 32 {
			return 0, errors.New("dovirpu: Exp-Golomb value too large")
		}
	}
	rest, err := r.bits(zeros)
	return 1<<zeros - 1 + rest, err
}

func (r *bitReader) se() (int64, error) {
	k, err := r.ue()
	v := int64(k>>1) + int64(k&1)
	if k&1 == 0 {
		v = -v
	}
	return v, err
}

// alignZeros skips to the next byte boundary; the skipped bits must be zero.
func (r *bitReader) alignZeros() error {
	for r.pos&7 != 0 {
		b, err := r.bits(1)
		if err != nil {
			return err
		}
		if b != 0 {
			return errors.New("dovirpu: non-zero alignment bit")
		}
	}
	return nil
}

type bitWriter struct {
	out  []byte
	nbit int
}

func (w *bitWriter) bits(v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		if w.nbit&7 == 0 {
			w.out = append(w.out, 0)
		}
		w.out[len(w.out)-1] |= byte(v>>i&1) << (7 - w.nbit&7)
		w.nbit++
	}
}

func (w *bitWriter) flag(b bool) {
	if b {
		w.bits(1, 1)
	} else {
		w.bits(0, 1)
	}
}

func (w *bitWriter) ue(v uint64) {
	n := 0
	for x := v + 1; x > 1; x >>= 1 {
		n++
	}
	w.bits(0, n)
	w.bits(v+1, n+1)
}

func (w *bitWriter) se(v int64) {
	if v > 0 {
		w.ue(uint64(2*v - 1))
	} else {
		w.ue(uint64(-2 * v))
	}
}

func (w *bitWriter) align() {
	for w.nbit&7 != 0 {
		w.bits(0, 1)
	}
}

// copyBits copies src's bits [from, to) to the writer.
func (w *bitWriter) copyBits(src []byte, from, to int) {
	r := bitReader{data: src, pos: from}
	for r.pos < to {
		n := min(to-r.pos, 32)
		v, _ := r.bits(n)
		w.bits(v, n)
	}
}

// unescape removes emulation-prevention bytes (00 00 03 → 00 00).
func unescape(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i, c := range b {
		if i >= 2 && b[i-2] == 0 && b[i-1] == 0 && c == 3 {
			continue
		}
		out = append(out, c)
	}
	return out
}

// escape inserts emulation-prevention bytes the way dovi_tool does: only past
// the first three bytes, so its output can be matched byte for byte.
func escape(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/64+4)
	for _, c := range b {
		if i := len(out); i > 2 && out[i-2] == 0 && out[i-1] == 0 && c <= 3 {
			out = append(out, 3)
		}
		out = append(out, c)
	}
	return out
}

var crcTable = func() (t [256]uint32) {
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// crc32MPEG2 is CRC-32/MPEG-2 (not reflected, no final XOR), which hash/crc32
// cannot compute because its tables are reflected.
func crc32MPEG2(b []byte) uint32 {
	c := uint32(0xFFFFFFFF)
	for _, x := range b {
		c = c<<8 ^ crcTable[byte(c>>24)^x]
	}
	return c
}
