package logutil

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseBodyMode(t *testing.T) {
	for _, value := range []string{"", "digest", "DIGEST", "full", "off"} {
		if _, err := ParseBodyMode(value); err != nil {
			t.Fatalf("ParseBodyMode(%q) error: %v", value, err)
		}
	}
	if _, err := ParseBodyMode("everything"); err == nil {
		t.Fatal("ParseBodyMode(everything) should fail")
	}
}

func TestParseBytes(t *testing.T) {
	tests := map[string]int{
		"0":      0,
		"1024":   1024,
		"64KiB":  64 << 10,
		"64kb":   64 << 10,
		"4M":     4 << 20,
		"4MiB":   4 << 20,
		"1GB":    1 << 30,
		"512 B":  512,
		" 128K ": 128 << 10,
	}
	for value, want := range tests {
		got, err := ParseBytes(value)
		if err != nil {
			t.Fatalf("ParseBytes(%q) error: %v", value, err)
		}
		if got != want {
			t.Fatalf("ParseBytes(%q) = %d, want %d", value, got, want)
		}
	}
	for _, value := range []string{"", "abc", "-1", "12XB", "K"} {
		if _, err := ParseBytes(value); err == nil {
			t.Fatalf("ParseBytes(%q) should fail", value)
		}
	}
}

func TestOptionsFromFlags(t *testing.T) {
	opts, err := OptionsFromFlags("full", "1MiB", "8MiB")
	if err != nil {
		t.Fatal(err)
	}
	if opts.BodyMode != BodyFull || opts.BodyLimit != 1<<20 || opts.QueueSize != 8<<20 {
		t.Fatalf("OptionsFromFlags = %+v", opts)
	}
	opts, err = OptionsFromFlags("", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if opts != DefaultOptions() {
		t.Fatalf("empty flags = %+v, want defaults %+v", opts, DefaultOptions())
	}
	if _, err := OptionsFromFlags("nope", "", ""); err == nil {
		t.Fatal("invalid --log-body should fail")
	}
	if _, err := OptionsFromFlags("", "12parsecs", ""); err == nil {
		t.Fatal("invalid --log-body-limit should fail")
	}
}

func TestBodyDigestSuppressesLargeBody(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, Options{BodyMode: BodyDigest, BodyLimit: 64})
	payload := strings.Repeat("secret-payload ", 1000)
	logger.Body("Body", []byte(payload))

	line := buf.String()
	if len(line) > 512 {
		t.Fatalf("digest line is %d bytes, want <= 512", len(line))
	}
	if !strings.Contains(line, "Body: bytes=") {
		t.Fatalf("digest line missing size prefix: %q", line)
	}
	if !strings.Contains(line, "sha256=") || !strings.Contains(line, "suppressed") {
		t.Fatalf("digest line missing digest marker: %q", line)
	}
	if strings.Contains(line, "secret-payload") {
		t.Fatalf("digest line leaked body content: %q", line)
	}
}

func TestBodyDigestKeepsSmallBody(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, Options{BodyMode: BodyDigest, BodyLimit: 64})
	logger.Body("Body", []byte(`{"hello":"world"}`))

	line := buf.String()
	if !strings.Contains(line, `{"hello":"world"}`) {
		t.Fatalf("small body was not kept: %q", line)
	}
	if !strings.Contains(line, "bytes=17") {
		t.Fatalf("small body line missing size: %q", line)
	}
}

func TestBodyFullKeepsLargeBody(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, Options{BodyMode: BodyFull, BodyLimit: 64})
	payload := strings.Repeat("x", 4096)
	logger.Body("Body", []byte(payload))

	if got := buf.String(); !strings.Contains(got, payload) {
		t.Fatalf("full mode dropped body content (len=%d)", len(got))
	}
}

func TestBodyOffKeepsNoContent(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, Options{BodyMode: BodyOff, BodyLimit: 64})
	logger.Body("Body", []byte("tiny"))

	line := buf.String()
	if strings.Contains(line, "tiny") {
		t.Fatalf("off mode leaked content: %q", line)
	}
	if !strings.Contains(line, "skipped") || !strings.Contains(line, "sha256=") {
		t.Fatalf("off mode missing digest marker: %q", line)
	}
}

func TestBodyStringMatchesBodyBytes(t *testing.T) {
	var fromBytes, fromString bytes.Buffer
	newLogger(&fromBytes, DefaultOptions()).Body("Body", []byte("same content"))
	newLogger(&fromString, DefaultOptions()).BodyString("Body", "same content")

	strip := func(s string) string { return s[strings.Index(s, " ")+1:] }
	if strip(fromBytes.String()) != strip(fromString.String()) {
		t.Fatalf("Body/BodyString differ:\n%s\n%s", fromBytes.String(), fromString.String())
	}
}

func TestNilLoggerIsSafe(t *testing.T) {
	var logger *Logger
	logger.Printf("ignored %d", 1)
	logger.Body("Body", []byte("ignored"))
	logger.BodyString("Body", "ignored")
}

// slowTarget blocks every Write so a test can prove the caller never waits.
type slowTarget struct {
	delay time.Duration
	mu    sync.Mutex
	lines int
	bytes int
}

func (s *slowTarget) Write(p []byte) (int, error) {
	time.Sleep(s.delay)
	s.mu.Lock()
	s.lines++
	s.bytes += len(p)
	s.mu.Unlock()
	return len(p), nil
}

func (s *slowTarget) rotate(time.Time, bool) error { return nil }
func (s *slowTarget) Close() error                 { return nil }
func (s *slowTarget) Name() string                 { return "slow" }

func TestQueueWriteNeverBlocksOnSlowDisk(t *testing.T) {
	target := &slowTarget{delay: 200 * time.Millisecond}
	queue := newQueueWriter(target, Options{BufferSize: 64 << 10, QueueSize: 1 << 20, FlushEvery: time.Hour})
	defer queue.Close()

	line := bytes.Repeat([]byte("x"), 1024)
	start := time.Now()
	for range 200 {
		n, err := queue.Write(line)
		if err != nil || n != len(line) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(line))
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("200 writes took %s, want non-blocking (< 100ms)", elapsed)
	}
}

func TestQueueDropsAndCountsWhenFull(t *testing.T) {
	target := &slowTarget{delay: 20 * time.Millisecond}
	var warnings []string
	var warnMu sync.Mutex
	queue := newQueueWriter(target, Options{BufferSize: 4 << 10, QueueSize: 4 << 10, FlushEvery: time.Hour})
	queue.warnf = func(format string, args ...any) {
		warnMu.Lock()
		warnings = append(warnings, format)
		warnMu.Unlock()
	}
	defer queue.Close()

	line := bytes.Repeat([]byte("x"), 1024)
	for range 500 {
		if n, err := queue.Write(line); err != nil || n != len(line) {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(line))
		}
	}
	lines, droppedBytes := queue.Dropped()
	if lines == 0 {
		t.Fatalf("expected dropped lines, got none (pending=%d queue=%d)", queue.pending.Load(), queue.queueSize)
	}
	if droppedBytes == 0 {
		t.Fatal("expected dropped bytes to be counted")
	}

	queue.reportDropped()
	warnMu.Lock()
	defer warnMu.Unlock()
	if len(warnings) == 0 || !strings.Contains(warnings[0], "log queue full") {
		t.Fatalf("missing drop warning, got %v", warnings)
	}
	if after, _ := queue.Dropped(); after != 0 {
		t.Fatalf("reportDropped did not reset the counter: %d", after)
	}
}

func TestQueueCloseFlushesEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	logger, closer, err := OpenAppendWith(path, Options{FlushEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		logger.Printf("entry %03d", i)
	}
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		if want := fmt.Sprintf("entry %03d", i); !strings.Contains(string(data), want) {
			t.Fatalf("%q missing after Close (log has %d bytes)", want, len(data))
		}
	}
}

func TestQueueRotationDoesNotSplitBufferedBurst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy.log")
	logger, closer, err := OpenAppendWith(path, Options{BodyMode: BodyFull, FlushEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	queue, ok := closer.(*queueWriter)
	if !ok {
		t.Fatalf("closer is %T, want *queueWriter", closer)
	}

	logger.Printf("before-1")
	logger.Printf("before-2")
	now := time.Now()
	if err := queue.rotateNow(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	logger.Printf("after-1")
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}

	oldSegment := segmentPath(path, now.Truncate(time.Hour))
	newSegment := segmentPath(path, now.Add(time.Hour).Truncate(time.Hour))

	oldData, err := os.ReadFile(oldSegment)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := os.ReadFile(newSegment)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(oldData), "before-1") || !strings.Contains(string(oldData), "before-2") {
		t.Fatalf("pre-rotation lines missing from old segment: %q", oldData)
	}
	if strings.Contains(string(oldData), "after-1") {
		t.Fatalf("post-rotation line leaked into old segment: %q", oldData)
	}
	if !strings.Contains(string(newData), "after-1") {
		t.Fatalf("post-rotation line missing from new segment: %q", newData)
	}
	target, err := os.Readlink(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Base(newSegment); target != want {
		t.Fatalf("symlink target = %q, want %q", target, want)
	}
}

type discardWriter struct{ bytes int64 }

func (d *discardWriter) Write(p []byte) (int, error) {
	d.bytes += int64(len(p))
	return len(p), nil
}

// BenchmarkBodyDigest measures the cost of reducing a 10 MiB body to one
// digest line: the body is hashed once and never copied into the log.
func BenchmarkBodyDigest(b *testing.B) {
	var sink discardWriter
	logger := newLogger(&sink, Options{BodyMode: BodyDigest, BodyLimit: DefaultBodyLimit})
	body := bytes.Repeat([]byte("x"), 10<<20)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		logger.Body("Body", body)
	}
}

// blockingTarget parks the writer goroutine until the test releases it.
type blockingTarget struct {
	release chan struct{}
}

func (b *blockingTarget) Write(p []byte) (int, error)  { <-b.release; return len(p), nil }
func (b *blockingTarget) rotate(time.Time, bool) error { return nil }
func (b *blockingTarget) Close() error                 { return nil }
func (b *blockingTarget) Name() string                 { return "blocking" }

func TestQueueAdmitsOneOversizedLineWhenIdle(t *testing.T) {
	target := &blockingTarget{release: make(chan struct{})}
	queue := newQueueWriter(target, Options{BufferSize: 64, QueueSize: 1 << 10, FlushEvery: time.Hour})
	defer func() {
		close(target.release)
		queue.Close()
	}()

	big := bytes.Repeat([]byte("x"), 8<<10)
	if n, err := queue.Write(big); err != nil || n != len(big) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(big))
	}
	if lines, _ := queue.Dropped(); lines != 0 {
		t.Fatalf("an idle queue must admit one oversized line, dropped %d", lines)
	}
	// The first line still occupies the writer, so a second one is refused.
	if n, err := queue.Write(big); err != nil || n != len(big) {
		t.Fatalf("second Write = (%d, %v)", n, err)
	}
	if lines, _ := queue.Dropped(); lines == 0 {
		t.Fatal("a second oversized line must be dropped while the first is in flight")
	}
}
