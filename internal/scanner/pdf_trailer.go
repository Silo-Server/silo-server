package scanner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

var errPDFTrailer = errors.New("invalid or unsupported PDF trailer")

const pdfNull = "null"

// pdfDocumentEncrypted inspects only cross-reference sections reached through
// startxref, Prev, and XRefStm. Their dictionaries are unencrypted, including
// when the cross-reference entries themselves are compressed (ISO 32000-1,
// sections 7.5.5 and 7.5.8). Page streams and indirect objects need no decoding.
func pdfDocumentEncrypted(file io.ReaderAt, size int64) (bool, error) {
	tail, err := readPDFTrailerWindow(file, size, max(0, size-maxPDFMetadataScanSize), maxPDFMetadataScanSize)
	if err != nil {
		return false, err
	}
	tail = bytes.TrimRight(tail, pdfWhitespace)
	tail, ok := bytes.CutSuffix(tail, []byte("%%EOF"))
	if !ok {
		return false, errPDFTrailer
	}
	idx := bytes.LastIndex(tail, []byte("startxref"))
	if idx < 0 || (idx > 0 && !bytes.ContainsRune([]byte(pdfWhitespace), rune(tail[idx-1]))) {
		return false, errPDFTrailer
	}
	p := pdfTrailerParser{data: tail[idx:]}
	if token := p.token(); token.kind != 'w' || token.text != "startxref" {
		return false, errPDFTrailer
	}
	offset, ok := pdfTrailerInteger(p.token())
	if !ok || offset <= 0 || p.token().kind != 0 || p.err != nil {
		return false, errPDFTrailer
	}

	type section struct {
		offset int64
		done   bool
	}
	pending := []section{{offset: offset}}
	seen := map[int64]byte{} // 1: visiting; 2: complete (hybrid files can share Prev).
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		offset = current.offset
		if current.done {
			seen[offset] = 2
			continue
		}
		if seen[offset] == 2 {
			continue
		}
		if seen[offset] == 1 || len(seen) >= 64 {
			return false, errPDFTrailer
		}
		seen[offset] = 1
		pending = append(pending, section{offset: offset, done: true})
		fields, err := readPDFTrailerAt(file, size, offset)
		if err != nil {
			return false, err
		}
		if encryption, exists := fields["Encrypt"]; exists && !encryption.isNull() {
			if encryption.kind != 'r' && encryption.kind != '<' {
				return false, errPDFTrailer
			}
			return true, nil
		}
		for _, key := range []string{"Prev", "XRefStm"} {
			value, exists := fields[key]
			if !exists || value.isNull() {
				continue
			}
			next, ok := pdfTrailerInteger(value)
			if !ok || next <= 0 || next >= size {
				return false, errPDFTrailer
			}
			pending = append(pending, section{offset: next})
		}
	}
	return false, nil
}

func readPDFTrailerWindow(file io.ReaderAt, size, offset, limit int64) ([]byte, error) {
	if offset < 0 || offset >= size {
		return nil, errPDFTrailer
	}
	return io.ReadAll(io.NewSectionReader(file, offset, min(size-offset, limit)))
}

func readPDFTrailerAt(file io.ReaderAt, size, offset int64) (map[string]pdfTrailerValue, error) {
	data, err := readPDFTrailerWindow(file, size, offset, 4096)
	if err != nil {
		return nil, err
	}
	p := pdfTrailerParser{data: data}
	first := p.token()
	if first.kind == 'w' && first.text == "xref" {
		// Cross-reference records are exactly 20 bytes (ISO 32000-1, 7.5.4).
		// Skip them by their declared count so large tables do not require
		// loading the entire table or searching its bytes for "trailer".
		for sections := 0; sections < 1024; sections++ {
			first = p.token()
			if first.kind == 'w' && first.text == "trailer" {
				data, err := readPDFTrailerWindow(file, size, offset+int64(p.pos), maxPDFMetadataScanSize)
				if err != nil {
					return nil, err
				}
				p = pdfTrailerParser{data: data}
				fields := p.dictionary(0)
				return fields, p.err
			}
			_, firstOK := pdfTrailerInteger(first)
			count, countOK := pdfTrailerInteger(p.token())
			if !firstOK || !countOK || !p.endLine() {
				return nil, errPDFTrailer
			}
			start := offset + int64(p.pos)
			if count > (size-start)/20 {
				return nil, errPDFTrailer
			}
			next := start + count*20
			data, err = readPDFTrailerWindow(file, size, next, 4096)
			if err != nil {
				return nil, err
			}
			offset = next
			p = pdfTrailerParser{data: data}
		}
		return nil, errPDFTrailer
	}
	object, objectOK := pdfTrailerInteger(first)
	generation, generationOK := pdfTrailerInteger(p.token())
	keyword := p.token()
	if !objectOK || object == 0 || !generationOK || generation > 65535 || keyword.kind != 'w' || keyword.text != "obj" {
		return nil, errPDFTrailer
	}
	data, err = readPDFTrailerWindow(file, size, offset+int64(p.pos), maxPDFMetadataScanSize)
	if err != nil {
		return nil, err
	}
	p = pdfTrailerParser{data: data}
	fields := p.dictionary(0)
	keyword = p.token()
	if p.err != nil || fields["Type"].kind != '/' || fields["Type"].text != "XRef" || keyword.kind != 'w' || keyword.text != "stream" {
		return nil, errPDFTrailer
	}
	return fields, p.err
}

type pdfTrailerValue struct {
	kind byte // w: keyword/number; /: name; s: string; r: reference; <: dictionary; [: array
	text string
}

type pdfTrailerParser struct {
	data []byte
	pos  int
	err  error
}

func (v pdfTrailerValue) isNull() bool {
	return v.kind == 'w' && v.text == pdfNull
}

func pdfTrailerInteger(value pdfTrailerValue) (int64, bool) {
	if value.kind != 'w' {
		return 0, false
	}
	n, err := strconv.ParseInt(value.text, 10, 64)
	return n, err == nil && n >= 0
}

func (p *pdfTrailerParser) dictionary(depth int) map[string]pdfTrailerValue {
	if depth >= 64 || p.token().kind != '<' {
		p.err = errPDFTrailer
		return nil
	}
	fields := map[string]pdfTrailerValue{}
	for p.err == nil {
		key := p.token()
		if key.kind == '>' {
			return fields
		}
		if key.kind != '/' {
			break
		}
		if _, duplicate := fields[key.text]; duplicate {
			break
		}
		fields[key.text] = p.value(depth + 1)
	}
	p.err = errPDFTrailer
	return nil
}

func (p *pdfTrailerParser) value(depth int) pdfTrailerValue {
	if depth >= 64 {
		p.err = errPDFTrailer
		return pdfTrailerValue{}
	}
	start := p.pos
	v := p.token()
	switch v.kind {
	case '<':
		p.pos = start
		p.dictionary(depth)
	case '[':
		for p.err == nil {
			start = p.pos
			if p.token().kind == ']' {
				return v
			}
			p.pos = start
			p.value(depth + 1)
		}
	case '/', 's':
	case 'w':
		if v.text == "true" || v.text == "false" || v.isNull() {
			return v
		}
		if !pdfTrailerNumber(v.text) {
			p.err = errPDFTrailer
			return v
		}
		if object, ok := pdfTrailerInteger(v); ok && object > 0 {
			start = p.pos
			generation, ok := pdfTrailerInteger(p.token())
			if ok && generation <= 65535 {
				if keyword := p.token(); keyword.kind == 'w' && keyword.text == "R" {
					return pdfTrailerValue{kind: 'r'}
				}
			}
			p.pos = start
		}
	default:
		p.err = errPDFTrailer
	}
	return v
}

func pdfTrailerNumber(text string) bool {
	digits, dots := 0, 0
	for i, b := range []byte(text) {
		switch {
		case b >= '0' && b <= '9':
			digits++
		case b == '.':
			dots++
		case i == 0 && (b == '+' || b == '-'):
		default:
			return false
		}
	}
	return digits > 0 && dots <= 1
}

func (p *pdfTrailerParser) token() pdfTrailerValue {
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		if bytes.ContainsRune([]byte(pdfWhitespace), rune(b)) {
			p.pos++
			continue
		}
		if b != '%' {
			break
		}
		for p.pos < len(p.data) && p.data[p.pos] != '\r' && p.data[p.pos] != '\n' {
			p.pos++
		}
	}
	if p.err != nil || p.pos >= len(p.data) {
		return pdfTrailerValue{}
	}
	b := p.data[p.pos]
	p.pos++
	switch b {
	case '[', ']':
		return pdfTrailerValue{kind: b}
	case '<', '>':
		if p.pos < len(p.data) && p.data[p.pos] == b {
			p.pos++
			return pdfTrailerValue{kind: b}
		}
		if b == '<' {
			for p.pos < len(p.data) {
				b = p.data[p.pos]
				p.pos++
				if b == '>' {
					return pdfTrailerValue{kind: 's'}
				}
				if fromHex(b) < 0 && !bytes.ContainsRune([]byte(pdfWhitespace), rune(b)) {
					break
				}
			}
		}
	case '(':
		for depth := 1; p.pos < len(p.data); {
			b = p.data[p.pos]
			p.pos++
			switch b {
			case '\\':
				if p.pos < len(p.data) {
					p.pos++
				}
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					return pdfTrailerValue{kind: 's'}
				}
			}
		}
	case '/':
		var name []byte
		for p.pos < len(p.data) && !isPDFTokenDelimiter(p.data[p.pos]) {
			b = p.data[p.pos]
			p.pos++
			if b == '#' {
				if p.pos+1 >= len(p.data) || fromHex(p.data[p.pos]) < 0 || fromHex(p.data[p.pos+1]) < 0 {
					p.err = errPDFTrailer
					return pdfTrailerValue{}
				}
				b = byte(fromHex(p.data[p.pos])<<4 | fromHex(p.data[p.pos+1]))
				p.pos += 2
			}
			name = append(name, b)
		}
		return pdfTrailerValue{kind: '/', text: string(name)}
	default:
		if !isPDFTokenDelimiter(b) {
			start := p.pos - 1
			for p.pos < len(p.data) && !isPDFTokenDelimiter(p.data[p.pos]) {
				p.pos++
			}
			return pdfTrailerValue{kind: 'w', text: string(p.data[start:p.pos])}
		}
	}
	p.err = errPDFTrailer
	return pdfTrailerValue{}
}

func (p *pdfTrailerParser) endLine() bool {
	for p.pos < len(p.data) {
		b := p.data[p.pos]
		p.pos++
		if b == '\r' || b == '\n' {
			if b == '\r' && p.pos < len(p.data) && p.data[p.pos] == '\n' {
				p.pos++
			}
			return true
		}
		if b != ' ' && b != '\t' {
			break
		}
	}
	p.err = fmt.Errorf("xref subsection header: %w", errPDFTrailer)
	return false
}
