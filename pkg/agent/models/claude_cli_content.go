package models

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// claudeContentStream reads the top-level "content" string out of the partial
// JSON the CLI streams for a structured answer. The answer is a JSON object
// ({"content": ..., "tool_calls": [...]}), so the deltas carry JSON, not prose:
// this extracts the answer field as it grows and never forwards the JSON.
//
// A fragment may end anywhere — inside an escape, inside a surrogate pair, or
// between two keys — so every piece of state survives Feed.
type claudeContentStream struct {
	emit func(string) bool

	// depth is 0 before the root object, 1 inside it, and >1 in nested values.
	// Only depth 1 is the answer object; a "content" key inside a tool's input
	// is not the answer.
	depth      int
	expectKey  bool
	contentKey bool
	contentVal bool

	inString   bool
	stringKind uint8
	key        strings.Builder
	escape     bool
	uWait      bool
	uDigits    [4]byte
	uLen       int
	high       rune

	pending  strings.Builder
	streamed strings.Builder
	done     bool
	stopped  bool
}

const (
	claudeStringOther uint8 = iota
	claudeStringKey
	claudeStringContent
)

func newClaudeContentStream(emit func(string) bool) *claudeContentStream {
	return &claudeContentStream{emit: emit}
}

// Feed consumes one partial_json fragment. It reports true when the consumer
// asked to stop, which the caller turns into a canceled turn.
func (s *claudeContentStream) Feed(fragment string) bool {
	if s.done || s.stopped {
		return s.stopped
	}
	for i := 0; i < len(fragment); i++ {
		s.step(fragment[i])
		if s.done || s.stopped {
			break
		}
	}
	if s.pending.Len() == 0 {
		return s.stopped
	}
	text := s.pending.String()
	s.pending.Reset()
	s.streamed.WriteString(text)
	if s.emit != nil && s.emit(text) {
		s.stopped = true
	}
	return s.stopped
}

// Content is every answer byte delivered so far.
func (s *claudeContentStream) Content() string { return s.streamed.String() }

// Streamed reports whether any answer text was delivered.
func (s *claudeContentStream) Streamed() bool { return s.streamed.Len() > 0 }

func (s *claudeContentStream) step(c byte) {
	if s.inString {
		s.stringByte(c)
		return
	}
	switch c {
	case '"':
		switch {
		case s.depth == 1 && s.expectKey:
			s.inString, s.stringKind = true, claudeStringKey
			s.key.Reset()
		case s.depth == 1 && s.contentVal:
			s.inString, s.stringKind = true, claudeStringContent
		default:
			s.inString, s.stringKind = true, claudeStringOther
		}
	case '{':
		if s.depth == 0 {
			s.depth, s.expectKey = 1, true
			return
		}
		s.depth++
	case '[':
		s.depth++
	case '}':
		if s.depth > 0 {
			s.depth--
		}
		if s.depth == 0 {
			s.done = true
		}
	case ']':
		if s.depth > 0 {
			s.depth--
		}
	case ',':
		if s.depth == 1 {
			s.expectKey, s.contentKey, s.contentVal = true, false, false
		}
	case ':':
		if s.depth == 1 && s.contentKey {
			s.contentVal = true
		}
		s.expectKey = false
	default:
		// content is a string or null; a null answer has nothing to stream.
		if s.depth == 1 && s.contentVal && !claudeJSONSpace(c) {
			s.done = true
		}
	}
}

func (s *claudeContentStream) stringByte(c byte) {
	if s.uWait {
		s.unicodeDigit(c)
		return
	}
	if s.escape {
		s.escapeByte(c)
		return
	}
	if c == '\\' {
		s.escape = true
		return
	}
	if c == '"' {
		s.inString = false
		switch s.stringKind {
		case claudeStringKey:
			if s.key.String() == "content" {
				s.contentKey = true
			}
			s.expectKey = false
		case claudeStringContent:
			if s.high != 0 {
				s.writeRune(utf8.RuneError)
				s.high = 0
			}
			s.done = true
		}
		return
	}
	s.writeByte(c)
}

func (s *claudeContentStream) escapeByte(c byte) {
	s.escape = false
	switch c {
	case '"', '\\', '/':
		s.writeByte(c)
	case 'b':
		s.writeByte('\b')
	case 'f':
		s.writeByte('\f')
	case 'n':
		s.writeByte('\n')
	case 'r':
		s.writeByte('\r')
	case 't':
		s.writeByte('\t')
	case 'u':
		s.uWait, s.uLen = true, 0
	default:
		// An invalid escape is not worth losing the answer over.
		s.writeByte(c)
	}
}

func (s *claudeContentStream) unicodeDigit(c byte) {
	s.uDigits[s.uLen] = c
	s.uLen++
	if s.uLen < 4 {
		return
	}
	s.uWait = false
	value, ok := claudeHex4(s.uDigits)
	if !ok {
		s.writeRune(utf8.RuneError)
		return
	}
	s.unicodeRune(rune(value))
}

func (s *claudeContentStream) unicodeRune(r rune) {
	if s.high != 0 {
		if r >= 0xDC00 && r <= 0xDFFF {
			s.writeRune(utf16.DecodeRune(s.high, r))
			s.high = 0
			return
		}
		s.writeRune(utf8.RuneError)
		s.high = 0
	}
	if r >= 0xD800 && r <= 0xDBFF {
		s.high = r
		return
	}
	s.writeRune(r)
}

func (s *claudeContentStream) writeByte(c byte) {
	switch s.stringKind {
	case claudeStringKey:
		s.key.WriteByte(c)
	case claudeStringContent:
		s.pending.WriteByte(c)
	}
}

func (s *claudeContentStream) writeRune(r rune) {
	var buf [utf8.UTFMax]byte
	n := utf8.EncodeRune(buf[:], r)
	switch s.stringKind {
	case claudeStringKey:
		s.key.Write(buf[:n])
	case claudeStringContent:
		s.pending.Write(buf[:n])
	}
}

func claudeHex4(digits [4]byte) (rune, bool) {
	var value rune
	for _, digit := range digits {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value |= rune(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value |= rune(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value |= rune(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func claudeJSONSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r':
		return true
	}
	return false
}
