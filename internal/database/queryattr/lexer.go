// Package queryattr accounts for database operations without retaining SQL or values.
package queryattr

import "strings"

// Scheme versions the fingerprint grammar and caller rules.
const Scheme = 1

// Fingerprint hashes the complete normalized statement without retaining text.
func Fingerprint(sql string) uint64 { h, _ := fingerprint(sql); return h }

const hashBasis uint64 = 14695981039346656037
const hashPrime uint64 = 1099511628211

type token struct {
	kind byte
	text string
}
type lexer struct {
	sql string
	pos int
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}
func word(c byte) bool  { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c >= 128 }
func digit(c byte) bool { return c >= '0' && c <= '9' }
func (l *lexer) next() token {
	s := l.sql
	for l.pos < len(s) {
		p := l.pos
		c := s[p]
		if c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' {
			l.pos++
			continue
		}
		if strings.HasPrefix(s[p:], "--") {
			l.pos += 2
			for l.pos < len(s) && s[l.pos] != '\n' {
				l.pos++
			}
			continue
		}
		if strings.HasPrefix(s[p:], "/*") {
			l.pos += 2
			depth := 1
			for l.pos < len(s) && depth > 0 {
				if strings.HasPrefix(s[l.pos:], "/*") {
					depth++
					l.pos += 2
				} else if strings.HasPrefix(s[l.pos:], "*/") {
					depth--
					l.pos += 2
				} else {
					l.pos++
				}
			}
			continue
		}
		break
	}
	if l.pos >= len(s) {
		return token{}
	}
	p := l.pos
	c := s[p]
	l.pos++
	// PostgreSQL's prefixed string forms are all values; escaped E strings alone
	// interpret backslash quoting. Dollar bodies never expose their contents.
	quote := c
	escaped := false
	if (lower(c) == 'e' || lower(c) == 'b' || lower(c) == 'x') && l.pos < len(s) && s[l.pos] == '\'' {
		quote = '\''
		escaped = lower(c) == 'e'
		l.pos++
	} else if lower(c) == 'u' && l.pos+1 < len(s) && s[l.pos] == '&' && s[l.pos+1] == '\'' {
		quote = '\''
		l.pos += 2
	}
	if quote == '\'' || c == '"' {
		for l.pos < len(s) {
			x := s[l.pos]
			l.pos++
			if escaped && x == '\\' {
				if l.pos < len(s) {
					l.pos++
				}
				continue
			}
			if x == quote {
				if l.pos < len(s) && s[l.pos] == quote {
					l.pos++
					continue
				}
				break
			}
		}
		if quote == '\'' {
			return token{kind: 'v'}
		}
		return token{'q', s[p:l.pos]}
	}
	if c == '$' {
		if l.pos < len(s) && digit(s[l.pos]) {
			for l.pos < len(s) && digit(s[l.pos]) {
				l.pos++
			}
			return token{kind: 'v'}
		}
		end := l.pos
		for end < len(s) && (word(s[end]) || digit(s[end])) {
			end++
		}
		if end < len(s) && s[end] == '$' {
			delim := s[p : end+1]
			l.pos = end + 1
			if n := strings.Index(s[l.pos:], delim); n >= 0 {
				l.pos += n + len(delim)
			} else {
				l.pos = len(s)
			}
			return token{kind: 'v'}
		}
	}
	if digit(c) || c == '.' && l.pos < len(s) && digit(s[l.pos]) {
		for l.pos < len(s) && (digit(s[l.pos]) || s[l.pos] == '.' || s[l.pos] == '_') {
			l.pos++
		}
		if l.pos < len(s) && (s[l.pos] == 'e' || s[l.pos] == 'E') {
			l.pos++
			if l.pos < len(s) && (s[l.pos] == '+' || s[l.pos] == '-') {
				l.pos++
			}
			for l.pos < len(s) && digit(s[l.pos]) {
				l.pos++
			}
		}
		if c == '0' && l.pos < len(s) && (s[l.pos] == 'x' || s[l.pos] == 'X' || s[l.pos] == 'o' || s[l.pos] == 'O' || s[l.pos] == 'b' || s[l.pos] == 'B') {
			l.pos++
			for l.pos < len(s) && (word(s[l.pos]) || digit(s[l.pos])) {
				l.pos++
			}
		}
		return token{kind: 'n'}
	}
	if word(c) {
		for l.pos < len(s) && (word(s[l.pos]) || digit(s[l.pos]) || s[l.pos] == '$') {
			l.pos++
		}
		return token{'w', s[p:l.pos]}
	}
	if strings.ContainsRune("+-*/<>=~!@#%^&|`?:", rune(c)) {
		for l.pos < len(s) && strings.ContainsRune("+-*/<>=~!@#%^&|`?:", rune(s[l.pos])) {
			if strings.HasPrefix(s[l.pos:], "--") || strings.HasPrefix(s[l.pos:], "/*") {
				break
			}
			l.pos++
		}
	}
	return token{'p', s[p:l.pos]}
}

func addToken(h uint64, t token) uint64 {
	if t.kind == 'n' {
		t.kind = 'v'
	}
	h = (h ^ uint64(t.kind)) * hashPrime
	n := len(t.text)
	for i := 0; i < 8; i++ {
		h = (h ^ uint64(byte(uint64(n)>>uint(i*8)))) * hashPrime
	}
	for i := 0; i < n; i++ {
		c := t.text[i]
		if t.kind == 'w' {
			c = lower(c)
		}
		h = (h ^ uint64(c)) * hashPrime
	}
	return h
}
func equalToken(a, b token) bool {
	if a.kind == 'n' {
		a.kind = 'v'
	}
	if b.kind == 'n' {
		b.kind = 'v'
	}
	return a.kind == b.kind && (a.text == b.text || a.kind == 'w' && strings.EqualFold(a.text, b.text))
}
func equalStream(s string, a, b, c, d int) bool {
	x := lexer{s[a:b], 0}
	y := lexer{s[c:d], 0}
	var prevX, prevY token
	for {
		u := nextNormalized(&x, prevX)
		v := nextNormalized(&y, prevY)
		prevX = u
		prevY = v
		if !equalToken(u, v) {
			return false
		}
		if u.kind == 0 {
			return true
		}
	}
}

// scalar consumes a value and optional casts, leaving the delimiter unread.
func scalar(l *lexer) bool {
	t := l.next()
	if t.text == "-" {
		t = l.next()
		if t.kind != 'n' {
			return false
		}
	}
	if t.kind != 'v' && t.kind != 'n' {
		return false
	}
	for {
		save := l.pos
		t = l.next()
		if t.text != "::" {
			l.pos = save
			return true
		}
		t = l.next()
		if t.kind != 'w' && t.kind != 'q' {
			return false
		}
	castParts:
		for {
			save = l.pos
			t = l.next()
			switch t.text {
			case ".":
				t = l.next()
				if t.kind != 'w' && t.kind != 'q' {
					return false
				}
			case "[":
				if l.next().text != "]" {
					return false
				}
			default:
				l.pos = save
				break castParts
			}
		}
	}
}
func collapseIN(l *lexer) (int32, bool) {
	if l.next().text != "(" {
		return 0, false
	}
	first := l.pos
	if !scalar(l) {
		return 0, false
	}
	end := l.pos
	n := int32(1)
	for {
		t := l.next()
		if t.text == ")" {
			return n, true
		}
		if t.text != "," {
			return 0, false
		}
		a := l.pos
		if !scalar(l) || !equalStream(l.sql, first, end, a, l.pos) {
			return 0, false
		}
		n++
	}
}
func tuple(l *lexer) (int, int, bool) {
	if l.next().text != "(" {
		return 0, 0, false
	}
	a := l.pos
	depth := 1
	for {
		before := l.pos
		t := l.next()
		if t.kind == 0 {
			return 0, 0, false
		}
		if t.text == "(" {
			depth++
		}
		if t.text == ")" {
			depth--
			if depth == 0 {
				return a, before, true
			}
		}
	}
}
func collapseValues(l *lexer) (uint64, int32, bool) {
	a, b, ok := tuple(l)
	if !ok {
		return 0, 0, false
	}
	n := int32(1)
	for {
		save := l.pos
		t := l.next()
		if t.text != "," {
			l.pos = save
			break
		}
		c, d, ok := tuple(l)
		if !ok || !equalStream(l.sql, a, b, c, d) {
			return 0, 0, false
		}
		n++
	}
	h := hashBasis
	probe := lexer{l.sql[a:b], 0}
	var prev token
	for {
		t := nextNormalized(&probe, prev)
		prev = t
		if t.kind == 0 {
			break
		}
		h = addToken(h, t)
	}
	return h, n, true
}
func fingerprint(s string) (uint64, int32) {
	l := lexer{s, 0}
	h := hashBasis
	var largest int32
	var prev token
	for {
		t := nextNormalized(&l, prev)
		if t.kind == 0 {
			break
		}
		if t.text == ";" {
			p := l
			if p.next().kind == 0 {
				break
			}
		}
		h = addToken(h, t)
		if t.kind == 'w' && strings.EqualFold(t.text, "in") {
			p := l
			if n, ok := collapseIN(&p); ok {
				h = addToken(h, token{'l', ""}) // Keep the scalar's cast/type structure.
				q := l
				_ = q.next()
				start := q.pos
				_ = scalar(&q)
				v := lexer{s[start:q.pos], 0}
				var scalarPrev token
				for {
					u := nextNormalized(&v, scalarPrev)
					scalarPrev = u
					if u.kind == 0 {
						break
					}
					h = addToken(h, u)
				}
				l = p
				largest = max(largest, n)
			}
		}
		if t.kind == 'w' && strings.EqualFold(t.text, "values") {
			p := l
			if shape, n, ok := collapseValues(&p); ok {
				h = addToken(h, token{'r', ""})
				for i := 0; i < 8; i++ {
					h = (h ^ uint64(byte(shape>>uint(i*8)))) * hashPrime
				}
				l = p
				largest = max(largest, n)
			}
		}
		prev = t
	}
	return h, largest
}

func nextNormalized(l *lexer, prev token) token {
	t := l.next()
	unary := prev.kind == 0 || prev.text == "(" || prev.text == "," || prev.kind == 'p' && strings.ContainsAny(prev.text, "=<>+-*/")
	if prev.kind == 'w' {
		for _, w := range [...]string{"select", "values", "limit", "offset", "then", "else", "when", "where", "having", "on", "and", "or", "not", "by", "returning"} {
			if strings.EqualFold(prev.text, w) {
				unary = true
				break
			}
		}
	}
	if t.text == "-" && unary {
		p := *l
		n := p.next()
		if n.kind == 'n' {
			*l = p
			return n
		}
	}
	return t
}
