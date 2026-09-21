package anthropic2openai

import (
	"strings"
	"unicode/utf8"
)

// stripSSEField returns the value of an SSE `field:` prefix on a single line,
// accepting both "field: value" and "field:value" forms. Ported from
// cc-switch proxy/sse.rs strip_sse_field.
func stripSSEField(line, field string) (string, bool) {
	if value, ok := strings.CutPrefix(line, field+": "); ok {
		return value, true
	}
	if value, ok := strings.CutPrefix(line, field+":"); ok {
		return value, true
	}
	return "", false
}

// takeSSEBlock pops one complete SSE block (terminated by a blank line) off
// the front of buffer, supporting both LF and CRLF delimiters. The earliest
// delimiter wins; on a tie the CRLF form is preferred, matching the Rust
// implementation. Ported from cc-switch proxy/sse.rs take_sse_block.
func takeSSEBlock(buffer *string) (string, bool) {
	bestPos := -1
	bestLen := 0
	for _, candidate := range []struct {
		delimiter string
		length    int
	}{{"\r\n\r\n", 4}, {"\n\n", 2}} {
		if pos := strings.Index(*buffer, candidate.delimiter); pos >= 0 {
			if bestPos < 0 || pos < bestPos {
				bestPos = pos
				bestLen = candidate.length
			}
		}
	}
	if bestPos < 0 {
		return "", false
	}
	block := (*buffer)[:bestPos]
	*buffer = (*buffer)[bestPos+bestLen:]
	return block, true
}

// appendUTF8Safe appends raw bytes to a UTF-8 buffer, correctly handling
// multi-byte characters that are split across chunk boundaries.
//
// remainder accumulates trailing bytes from the previous chunk that form an
// incomplete UTF-8 sequence (at most 3 bytes under normal operation). On each
// call the remainder is prepended to newBytes, the longest valid UTF-8 prefix
// is appended to buffer, and any trailing incomplete bytes are saved back into
// remainder for the next call.
//
// A defensive guard discards remainder via lossy conversion if it ever exceeds
// 3 bytes, which cannot happen with well-formed UTF-8 streams. Genuinely
// invalid bytes are flushed immediately as U+FFFD instead of accumulating.
// Ported from cc-switch proxy/sse.rs append_utf8_safe.
func appendUTF8Safe(buffer *strings.Builder, remainder *[]byte, newBytes []byte) {
	var input []byte
	if len(*remainder) == 0 {
		input = newBytes
	} else if len(*remainder) > 3 {
		// Defensive guard: remainder should never exceed 3 bytes (max
		// incomplete UTF-8 sequence is 3 bytes). If it does, the stream is
		// producing genuinely invalid bytes; flush them lossy and start fresh.
		writeLossy(buffer, *remainder)
		*remainder = (*remainder)[:0]
		input = newBytes
	} else {
		input = append(*remainder, newBytes...)
		*remainder = (*remainder)[:0]
	}

	pos := 0
	for pos < len(input) {
		b := input[pos]
		if b < utf8.RuneSelf {
			buffer.WriteByte(b)
			pos++
			continue
		}
		size := utf8SequenceLength(b)
		if size == 0 {
			// Invalid starter byte: emit U+FFFD and continue.
			buffer.WriteRune(utf8.RuneError)
			pos++
			continue
		}
		if pos+size > len(input) {
			// Sequence extends past the end of this chunk. If the bytes seen
			// so far form a valid prefix, stash them for the next chunk.
			if validUTF8Prefix(input[pos:]) {
				*remainder = append((*remainder)[:0], input[pos:]...)
				return
			}
			// Already invalid despite being short: flush the starter.
			buffer.WriteRune(utf8.RuneError)
			pos++
			continue
		}
		if r, got := utf8.DecodeRune(input[pos : pos+size]); r != utf8.RuneError || got != 1 {
			buffer.Write(input[pos : pos+size])
			pos += size
			continue
		}
		// Full sequence present but invalid (overlong, surrogate, bad
		// continuation): emit U+FFFD and resynchronize after the starter.
		buffer.WriteRune(utf8.RuneError)
		pos++
	}
}

// utf8SequenceLength returns the encoded length promised by a UTF-8 starter
// byte, or 0 when the byte can never start a valid sequence.
func utf8SequenceLength(b byte) int {
	switch {
	case b < 0x80:
		return 1
	case b >= 0xC2 && b <= 0xDF:
		return 2
	case b >= 0xE0 && b <= 0xEF:
		return 3
	case b >= 0xF0 && b <= 0xF4:
		return 4
	default:
		return 0
	}
}

// validUTF8Prefix reports whether the bytes of a truncated sequence seen so
// far could still grow into a valid UTF-8 sequence. Continuation ranges are
// narrowed for the constrained starters (0xE0, 0xED, 0xF0, 0xF4) so overlong
// and surrogate prefixes are rejected instead of buffered forever.
func validUTF8Prefix(bytes []byte) bool {
	if len(bytes) == 0 {
		return false
	}
	size := utf8SequenceLength(bytes[0])
	if size == 0 || size <= len(bytes) {
		return false
	}
	for i := 1; i < len(bytes); i++ {
		if !continuationInRange(bytes[0], bytes[i], i) {
			return false
		}
	}
	return true
}

// continuationInRange reports whether the i-th continuation byte (1-based,
// after the starter) is legal for the given starter byte.
func continuationInRange(starter, c byte, i int) bool {
	if c < 0x80 || c > 0xBF {
		return false
	}
	switch starter {
	case 0xE0:
		return i != 1 || c >= 0xA0
	case 0xED:
		return i != 1 || c <= 0x9F
	case 0xF0:
		return i != 1 || c >= 0x90
	case 0xF4:
		return i != 1 || c <= 0x8F
	}
	return true
}

// writeLossy appends bytes to buffer, replacing every invalid byte or
// sequence with U+FFFD (one per invalid byte, matching Rust's
// String::from_utf8_lossy over junk bytes).
func writeLossy(buffer *strings.Builder, bytes []byte) {
	for pos := 0; pos < len(bytes); {
		r, size := utf8.DecodeRune(bytes[pos:])
		if r == utf8.RuneError && size <= 1 {
			buffer.WriteRune(utf8.RuneError)
			pos++
			continue
		}
		buffer.WriteRune(r)
		pos += size
	}
}
