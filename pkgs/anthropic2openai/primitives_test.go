package anthropic2openai

import (
	"encoding/json"
	"strings"
	"testing"
)

// Ported from cc-switch proxy/sse.rs tests.

func TestStripSSEFieldAcceptsOptionalSpace(t *testing.T) {
	if value, ok := stripSSEField(`data: {"ok":true}`, "data"); !ok || value != `{"ok":true}` {
		t.Fatalf("stripSSEField with space = %q, %v", value, ok)
	}
	if value, ok := stripSSEField(`data:{"ok":true}`, "data"); !ok || value != `{"ok":true}` {
		t.Fatalf("stripSSEField without space = %q, %v", value, ok)
	}
	if value, ok := stripSSEField("event: message_start", "event"); !ok || value != "message_start" {
		t.Fatalf("stripSSEField event = %q, %v", value, ok)
	}
	if value, ok := stripSSEField("event:message_start", "event"); !ok || value != "message_start" {
		t.Fatalf("stripSSEField event no space = %q, %v", value, ok)
	}
	if _, ok := stripSSEField("id:1", "data"); ok {
		t.Fatal("wrong field must not match")
	}
}

func TestTakeSSEBlockSupportsLFDelimiters(t *testing.T) {
	buffer := "data: {\"ok\":true}\n\nrest"
	block, ok := takeSSEBlock(&buffer)
	if !ok || block != "data: {\"ok\":true}" {
		t.Fatalf("block = %q, %v", block, ok)
	}
	if buffer != "rest" {
		t.Fatalf("buffer = %q", buffer)
	}
}

func TestTakeSSEBlockSupportsCRLFDelimiters(t *testing.T) {
	buffer := "data: {\"ok\":true}\r\n\r\nrest"
	block, ok := takeSSEBlock(&buffer)
	if !ok || block != "data: {\"ok\":true}" {
		t.Fatalf("block = %q, %v", block, ok)
	}
	if buffer != "rest" {
		t.Fatalf("buffer = %q", buffer)
	}
}

func TestASCIIPassthrough(t *testing.T) {
	var buf strings.Builder
	var rem []byte
	appendUTF8Safe(&buf, &rem, []byte("hello world"))
	if buf.String() != "hello world" || len(rem) != 0 {
		t.Fatalf("buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestCompleteMultibyteInSingleChunk(t *testing.T) {
	var buf strings.Builder
	var rem []byte
	appendUTF8Safe(&buf, &rem, []byte("你好世界"))
	if buf.String() != "你好世界" || len(rem) != 0 {
		t.Fatalf("buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestSplitMultibyteAcrossTwoChunks(t *testing.T) {
	// "你" = E4 BD A0 (3 bytes)
	bytes := []byte("你")
	if len(bytes) != 3 {
		t.Fatalf("unexpected byte length %d", len(bytes))
	}
	var buf strings.Builder
	var rem []byte

	// Chunk 1: first 2 bytes (incomplete)
	appendUTF8Safe(&buf, &rem, bytes[:2])
	if buf.String() != "" || len(rem) != 2 {
		t.Fatalf("chunk1 buf = %q, rem = %v", buf.String(), rem)
	}

	// Chunk 2: last byte completes the character
	appendUTF8Safe(&buf, &rem, bytes[2:])
	if buf.String() != "你" || len(rem) != 0 {
		t.Fatalf("chunk2 buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestSplitFourByteCharAcrossChunks(t *testing.T) {
	// 😀 = F0 9F 98 80 (4 bytes)
	bytes := []byte("😀")
	if len(bytes) != 4 {
		t.Fatalf("unexpected byte length %d", len(bytes))
	}
	var buf strings.Builder
	var rem []byte

	appendUTF8Safe(&buf, &rem, bytes[:1])
	if buf.String() != "" || len(rem) != 1 {
		t.Fatalf("chunk1 buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, bytes[1:2])
	if buf.String() != "" || len(rem) != 2 {
		t.Fatalf("chunk2 buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, bytes[2:3])
	if buf.String() != "" || len(rem) != 3 {
		t.Fatalf("chunk3 buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, bytes[3:])
	if buf.String() != "😀" || len(rem) != 0 {
		t.Fatalf("chunk4 buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestMixedASCIISplitMultibyte(t *testing.T) {
	// "hi你" = 68 69 E4 BD A0
	all := []byte("hi你")
	if len(all) != 5 {
		t.Fatalf("unexpected byte length %d", len(all))
	}
	var buf strings.Builder
	var rem []byte

	appendUTF8Safe(&buf, &rem, all[:3])
	if buf.String() != "hi" || len(rem) != 1 {
		t.Fatalf("chunk1 buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, all[3:])
	if buf.String() != "hi你" || len(rem) != 0 {
		t.Fatalf("chunk2 buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestMultipleSplitCharactersInSequence(t *testing.T) {
	bytes := []byte("你好") // E4 BD A0 E5 A5 BD
	var buf strings.Builder
	var rem []byte

	// Split in the middle: first char complete + 1 byte of second
	appendUTF8Safe(&buf, &rem, bytes[:4])
	if buf.String() != "你" || len(rem) != 1 {
		t.Fatalf("chunk1 buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, bytes[4:])
	if buf.String() != "你好" || len(rem) != 0 {
		t.Fatalf("chunk2 buf = %q, rem = %v", buf.String(), rem)
	}
}

func TestEmptyChunksAreHarmless(t *testing.T) {
	var buf strings.Builder
	var rem []byte

	appendUTF8Safe(&buf, &rem, nil)
	if buf.String() != "" || len(rem) != 0 {
		t.Fatalf("empty chunk buf = %q, rem = %v", buf.String(), rem)
	}
	appendUTF8Safe(&buf, &rem, []byte("ok"))
	if buf.String() != "ok" {
		t.Fatalf("buf = %q", buf.String())
	}
	appendUTF8Safe(&buf, &rem, nil)
	if buf.String() != "ok" {
		t.Fatalf("buf = %q", buf.String())
	}
}

func TestSSEJSONWithChineseSplitAtBoundary(t *testing.T) {
	jsonLine := "data: {\"text\":\"你好\"}\n\n"
	bytes := []byte(jsonLine)

	// Find where "你" starts in the byte stream and split inside it.
	splitPoint := strings.Index(jsonLine, "你") + 1
	var buf strings.Builder
	var rem []byte

	appendUTF8Safe(&buf, &rem, bytes[:splitPoint])
	appendUTF8Safe(&buf, &rem, bytes[splitPoint:])

	if buf.String() != jsonLine || len(rem) != 0 {
		t.Fatalf("buf = %q, rem = %v", buf.String(), rem)
	}

	// Verify the buffer can be parsed as SSE with valid JSON.
	line, _, _ := strings.Cut(buf.String(), "\n")
	data, ok := stripSSEField(line, "data")
	if !ok {
		t.Fatal("data field not found")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(data), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["text"] != "你好" {
		t.Fatalf("text = %v", parsed["text"])
	}
}

func TestInvalidBytesFlushedImmediatelyNotAccumulated(t *testing.T) {
	// 0xFF is never valid in UTF-8 – it should be replaced immediately,
	// not stashed in remainder.
	var buf strings.Builder
	var rem []byte

	appendUTF8Safe(&buf, &rem, []byte("hi\xFFok"))
	if len(rem) != 0 {
		t.Fatalf("remainder should be empty after invalid byte: %v", rem)
	}
	if !strings.Contains(buf.String(), "hi") {
		t.Fatal("valid prefix must be present")
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Fatal("valid suffix must be present")
	}
	if !strings.ContainsRune(buf.String(), '\uFFFD') {
		t.Fatal("invalid byte must produce U+FFFD")
	}
}

func TestInvalidByteInSlowPathFlushedImmediately(t *testing.T) {
	var buf strings.Builder
	var rem []byte

	// Prime remainder with an incomplete sequence (first byte of "你")
	appendUTF8Safe(&buf, &rem, []byte("你")[:1])
	if len(rem) != 1 {
		t.Fatalf("remainder = %v", rem)
	}

	// Next chunk starts with an invalid byte – the stale remainder and the
	// invalid byte should both be flushed, not accumulated.
	appendUTF8Safe(&buf, &rem, []byte("\xFFworld"))
	if len(rem) != 0 {
		t.Fatalf("remainder should be empty: %v", rem)
	}
	if !strings.Contains(buf.String(), "world") {
		t.Fatal("valid data after invalid byte must appear")
	}
}

func TestDefensiveGuardFlushesOversizedRemainder(t *testing.T) {
	var buf strings.Builder
	var rem []byte

	// Manually inject 4 invalid bytes into remainder to trigger the >3 guard.
	rem = append(rem, 0x80, 0x80, 0x80, 0x80)
	appendUTF8Safe(&buf, &rem, []byte("hello"))
	if len(rem) != 0 {
		t.Fatal("remainder must be empty after guard flush")
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatal("valid data after guard flush must appear")
	}
	// The 4 invalid bytes each produce a U+FFFD
	if count := strings.Count(buf.String(), "\uFFFD"); count != 4 {
		t.Fatalf("replacement count = %d, want 4", count)
	}
}

// Ported from cc-switch proxy/json_canonical.rs tests.

func TestCanonicalJSONStringSortsNestedObjectKeys(t *testing.T) {
	left := map[string]any{
		"b": json.Number("2"),
		"a": map[string]any{
			"d": true,
			"c": []any{json.Number("3"), map[string]any{"z": json.Number("1"), "y": json.Number("2")}},
		},
	}
	right := map[string]any{
		"a": map[string]any{
			"c": []any{json.Number("3"), map[string]any{"y": json.Number("2"), "z": json.Number("1")}},
			"d": true,
		},
		"b": json.Number("2"),
	}

	if canonicalJSONString(left) != canonicalJSONString(right) {
		t.Fatalf("canonical forms differ:\n%s\n%s", canonicalJSONString(left), canonicalJSONString(right))
	}
	if shortValueHash(left) != shortValueHash(right) {
		t.Fatal("hashes differ for equal values")
	}
}

func TestCanonicalizeJSONStringIfParseableSortsKeysAndRemovesWhitespace(t *testing.T) {
	if got := canonicalizeJSONStringIfParseable(`{ "b": 2, "a": 1 }`); got != `{"a":1,"b":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalizeJSONStringIfParseablePreservesPlainText(t *testing.T) {
	if got := canonicalizeJSONStringIfParseable("plain text"); got != "plain text" {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalizeToolArgumentsStrCoercesEmptyToObject(t *testing.T) {
	if got := canonicalizeToolArgumentsStr(""); got != "{}" {
		t.Fatalf("got %q", got)
	}
	if got := canonicalizeToolArgumentsStr("   "); got != "{}" {
		t.Fatalf("got %q", got)
	}
	if got := canonicalizeToolArgumentsStr("\n\t"); got != "{}" {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalizeToolArgumentsStrCanonicalizesValidJSON(t *testing.T) {
	if got := canonicalizeToolArgumentsStr(`{ "b": 2, "a": 1 }`); got != `{"a":1,"b":2}` {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalizeToolArgumentsHandlesFieldVariants(t *testing.T) {
	// Missing field -> empty object.
	if got := canonicalizeToolArguments(nil); got != "{}" {
		t.Fatalf("nil -> %q", got)
	}
	// Empty string field -> empty object.
	if got := canonicalizeToolArguments(""); got != "{}" {
		t.Fatalf("empty string -> %q", got)
	}
	// String field with JSON -> canonicalized.
	if got := canonicalizeToolArguments(`{"b":2,"a":1}`); got != `{"a":1,"b":2}` {
		t.Fatalf("json string -> %q", got)
	}
	// Structured (non-string) field -> canonical serialization.
	if got := canonicalizeToolArguments(map[string]any{"b": json.Number("2"), "a": json.Number("1")}); got != `{"a":1,"b":2}` {
		t.Fatalf("structured -> %q", got)
	}
}
