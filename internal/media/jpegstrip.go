package media

import (
	"bytes"
	"encoding/binary"
)

func stripJPEG(data []byte) ([]byte, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false
	}
	out := make([]byte, 0, len(data))
	out = append(out, 0xFF, 0xD8)
	pos := 2
	for {
		for pos+1 < len(data) && data[pos] == 0xFF && data[pos+1] == 0xFF {
			pos++
		}
		if pos+2 > len(data) || data[pos] != 0xFF {
			return nil, false
		}
		m := data[pos+1]
		pos += 2
		if m == 0xD9 {
			return append(out, 0xFF, 0xD9), true
		}
		if m >= 0xD0 && m <= 0xD7 {
			out = append(out, 0xFF, m)
			continue
		}
		if pos+2 > len(data) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(data[pos:]))
		if n < 2 || pos+n > len(data) {
			return nil, false
		}
		seg := data[pos : pos+n]
		pos += n
		switch {
		case m == 0xE0:
			out = append(out, minimalJFIF(seg[2:])...)
		case m == 0xEE && bytes.HasPrefix(seg[2:], []byte("Adobe")):
			out = append(append(out, 0xFF, m), seg...)
		case m >= 0xE1 && m <= 0xEF, m == 0xFE:
		case keptSegment(m):
			out = append(append(out, 0xFF, m), seg...)
			if m == 0xDA {
				end := scanEnd(data, pos)
				if end < 0 {
					return nil, false
				}
				out = append(out, data[pos:end]...)
				pos = end
			}
		default:
			return nil, false
		}
	}
}

func keptSegment(m byte) bool {
	switch {
	case m >= 0xC0 && m <= 0xCF:
		return m != 0xC8
	case m == 0xDA, m == 0xDB, m == 0xDC, m == 0xDD:
		return true
	}
	return false
}

func scanEnd(data []byte, pos int) int {
	for i := pos; i+1 < len(data); i++ {
		if data[i] != 0xFF {
			continue
		}
		if next := data[i+1]; next == 0x00 || (next >= 0xD0 && next <= 0xD7) {
			i++
			continue
		}
		return i
	}
	return -1
}

func minimalJFIF(payload []byte) []byte {
	if len(payload) < 12 || !bytes.HasPrefix(payload, []byte("JFIF\x00")) {
		return nil
	}
	seg := []byte{0xFF, 0xE0, 0x00, 0x10}
	seg = append(seg, payload[:12]...)
	return append(seg, 0x00, 0x00)
}
