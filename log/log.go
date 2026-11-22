// Package logutil writes the llm-proxy full log.
//
// Two properties matter more than formatting:
//
//   - A request must never block on disk. Writes go through a bounded queue and
//     a single writer goroutine, so a multi-megabyte request body cannot hold
//     the request path behind a mutex or a slow filesystem.
//   - A multi-megabyte body must not land in the log by default. Body lines are
//     reduced to size + digest unless the body is small or --log-body=full is
//     set; the log stays greppable and stops growing at hundreds of megabytes
//     per hour.
//
// Rotation keeps its existing contract: hourly segments named
// <path>.<YYYYMMDDHH>, <path> itself a symlink to the current segment, and the
// previous hour retained. The queue's writer goroutine owns rotation so a
// buffered line can never spill into the next hour's segment.
package logutil

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xhd2015/dot-pkgs/go-pkgs/pathfmt"
)

const (
	timeLayout   = "2006/01/02 15:04:05"
	hourLayout   = "2006010215"
	digestPrefix = 8 // bytes of sha256 kept in a body line

	// DefaultBodyLimit is the largest body logged verbatim in digest mode.
	DefaultBodyLimit = 64 << 10 // 64 KiB
	// DefaultBufferSize is the bufio buffer between the queue and the file.
	DefaultBufferSize = 256 << 10 // 256 KiB
	// DefaultQueueSize bounds bytes waiting in memory for the writer.
	DefaultQueueSize = 4 << 20 // 4 MiB
	// DefaultFlushEvery bounds how long a line may sit in the buffer.
	DefaultFlushEvery = 250 * time.Millisecond

	droppedReportEvery = 10 * time.Second
)

// BodyMode selects what a body line keeps.
type BodyMode string

const (
	// BodyDigest logs bodies up to Options.BodyLimit verbatim and larger ones
	// as size + sha256.
	BodyDigest BodyMode = "digest"
	// BodyFull logs every body verbatim (the pre-existing behavior).
	BodyFull BodyMode = "full"
	// BodyOff logs no body content at all, only size + sha256.
	BodyOff BodyMode = "off"
)

// Options configures one log sink.
type Options struct {
	BodyMode   BodyMode
	BodyLimit  int
	BufferSize int
	QueueSize  int
	FlushEvery time.Duration
}

// DefaultOptions is the configuration used when the CLI passes no log flags.
func DefaultOptions() Options {
	return Options{
		BodyMode:   BodyDigest,
		BodyLimit:  DefaultBodyLimit,
		BufferSize: DefaultBufferSize,
		QueueSize:  DefaultQueueSize,
		FlushEvery: DefaultFlushEvery,
	}
}

// normalized fills unset fields from the defaults so a zero Options works.
func (o Options) normalized() Options {
	def := DefaultOptions()
	switch o.BodyMode {
	case BodyDigest, BodyFull, BodyOff:
	default:
		o.BodyMode = def.BodyMode
	}
	if o.BodyLimit <= 0 {
		o.BodyLimit = def.BodyLimit
	}
	if o.BufferSize <= 0 {
		o.BufferSize = def.BufferSize
	}
	if o.QueueSize <= 0 {
		o.QueueSize = def.QueueSize
	}
	if o.FlushEvery <= 0 {
		o.FlushEvery = def.FlushEvery
	}
	return o
}

// ParseBodyMode validates a --log-body value.
func ParseBodyMode(value string) (BodyMode, error) {
	switch BodyMode(strings.ToLower(strings.TrimSpace(value))) {
	case "":
		return DefaultOptions().BodyMode, nil
	case BodyDigest:
		return BodyDigest, nil
	case BodyFull:
		return BodyFull, nil
	case BodyOff:
		return BodyOff, nil
	default:
		return "", fmt.Errorf("--log-body=%s is not one of digest, full, off", value)
	}
}

// ParseBytes parses a byte size: a plain integer, or a suffix from B, K/KB/KiB,
// M/MB/MiB, G/GB/GiB (case-insensitive, binary multiples).
func ParseBytes(value string) (int, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(value))
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	multiplier := 1
	for _, suffix := range []struct {
		suffix     string
		multiplier int
	}{
		{"GIB", 1 << 30}, {"GB", 1 << 30}, {"G", 1 << 30},
		{"MIB", 1 << 20}, {"MB", 1 << 20}, {"M", 1 << 20},
		{"KIB", 1 << 10}, {"KB", 1 << 10}, {"K", 1 << 10},
		{"B", 1},
	} {
		if strings.HasSuffix(trimmed, suffix.suffix) {
			multiplier = suffix.multiplier
			trimmed = strings.TrimSuffix(trimmed, suffix.suffix)
			break
		}
	}
	if trimmed == "" {
		return 0, fmt.Errorf("size %q has no number", value)
	}
	number, err := strconv.Atoi(strings.TrimSpace(trimmed))
	if err != nil || number < 0 {
		return 0, fmt.Errorf("size %q is not a non-negative integer", value)
	}
	if number > (1<<62)/multiplier {
		return 0, fmt.Errorf("size %q is too large", value)
	}
	return number * multiplier, nil
}

// OptionsFromFlags resolves the three --log-* flag values into Options.
func OptionsFromFlags(bodyMode, bodyLimit, queueSize string) (Options, error) {
	opts := DefaultOptions()
	mode, err := ParseBodyMode(bodyMode)
	if err != nil {
		return Options{}, err
	}
	opts.BodyMode = mode
	if strings.TrimSpace(bodyLimit) != "" {
		limit, err := ParseBytes(bodyLimit)
		if err != nil {
			return Options{}, fmt.Errorf("--log-body-limit: %w", err)
		}
		opts.BodyLimit = limit
	}
	if strings.TrimSpace(queueSize) != "" {
		size, err := ParseBytes(queueSize)
		if err != nil {
			return Options{}, fmt.Errorf("--log-buffer: %w", err)
		}
		opts.QueueSize = size
	}
	return opts, nil
}

var (
	optionsMu sync.RWMutex
	options   = DefaultOptions()
)

// Configure sets the process-wide options used by OpenAppend. The CLI calls it
// once after flag parsing; concurrent proxy modes then share one policy.
func Configure(opts Options) {
	optionsMu.Lock()
	options = opts.normalized()
	optionsMu.Unlock()
}

// CurrentOptions reports the process-wide options.
func CurrentOptions() Options {
	optionsMu.RLock()
	defer optionsMu.RUnlock()
	return options
}

// Logger writes formatted lines and body lines to one sink.
type Logger struct {
	w     io.Writer
	mode  BodyMode
	limit int
}

// New wraps w with the default body policy; the writer is used synchronously.
// OpenAppend is the asynchronous, rotating entry point used by the proxies.
func New(w io.Writer) *Logger {
	if w == nil {
		return nil
	}
	opts := DefaultOptions()
	return &Logger{w: w, mode: opts.BodyMode, limit: opts.BodyLimit}
}

func newLogger(w io.Writer, opts Options) *Logger {
	if w == nil {
		return nil
	}
	return &Logger{w: w, mode: opts.BodyMode, limit: opts.BodyLimit}
}

// Printf writes one timestamped line.
func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	line := fmt.Appendf(nil, "%s ", time.Now().Format(timeLayout))
	line = fmt.Appendf(line, format, args...)
	line = append(line, '\n')
	_, _ = l.w.Write(line)
}

// Body writes one body line. The body is kept verbatim only when the mode and
// size allow it; otherwise the line carries size and digest instead.
func (l *Logger) Body(label string, body []byte) {
	if l == nil || l.w == nil {
		return
	}
	l.writeBody(label, len(body),
		func() string {
			sum := sha256.Sum256(body)
			return hex.EncodeToString(sum[:digestPrefix])
		},
		func(dst []byte) []byte { return append(dst, body...) })
}

// BodyString is Body for an already-decoded string.
func (l *Logger) BodyString(label, body string) {
	if l == nil || l.w == nil {
		return
	}
	l.writeBody(label, len(body),
		func() string {
			sum := sha256.Sum256([]byte(body))
			return hex.EncodeToString(sum[:digestPrefix])
		},
		func(dst []byte) []byte { return append(dst, body...) })
}

func (l *Logger) writeBody(label string, size int, digest func() string, appendContent func([]byte) []byte) {
	line := fmt.Appendf(nil, "%s %s: bytes=%d ", time.Now().Format(timeLayout), label, size)
	switch {
	case l.mode == BodyFull || (l.mode == BodyDigest && size <= l.limit):
		line = appendContent(line)
	case l.mode == BodyOff:
		line = fmt.Appendf(line, "sha256=%s skipped (--log-body=full to keep)\n", digest())
	default:
		line = fmt.Appendf(line, "sha256=%s suppressed (--log-body=full to keep)\n", digest())
	}
	if len(line) == 0 || line[len(line)-1] != '\n' {
		line = append(line, '\n')
	}
	_, _ = l.w.Write(line)
}

func (l *Logger) LogHeaders(headers http.Header) {
	if l == nil {
		return
	}
	LogHeaders(headers, l.Printf)
}

func LogHeaders(headers http.Header, logf func(format string, args ...any)) {
	if logf == nil {
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		logf("Header: %s: %s", key, RedactHeaderValue(key, headers.Values(key)))
	}
}

func RedactHeaderValue(key string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	lowerKey := strings.ToLower(key)
	if lowerKey == "authorization" || lowerKey == "proxy-authorization" || lowerKey == "cookie" || lowerKey == "set-cookie" {
		return "<redacted>"
	}
	if strings.Contains(lowerKey, "token") || strings.Contains(lowerKey, "secret") || strings.Contains(lowerKey, "api-key") {
		return "<redacted>"
	}
	return strings.Join(values, ", ")
}

func ExpandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	return pathfmt.Expand(path), nil
}

// DefaultLogFile is the full-log destination used when --log is not passed.
const DefaultLogFile = "/tmp/llm-proxy.log"

// LogOff is the --log value that disables file logging entirely.
const LogOff = "off"

// ResolveLogFile maps the --log flag value to the file to open.
// It returns the resolved path and whether the default was applied (so the
// caller can print a notice). A false resolved value means no file logging.
func ResolveLogFile(flagValue string) (path string, defaulted bool) {
	switch flagValue {
	case LogOff:
		return "", false
	case "":
		return DefaultLogFile, true
	default:
		return flagValue, false
	}
}

// OpenAppend opens an asynchronous, hourly-rotated log using the process-wide
// options. A nil Logger means file logging is disabled.
func OpenAppend(path string) (*Logger, io.Closer, error) {
	return OpenAppendWith(path, CurrentOptions())
}

// OpenAppendWith is OpenAppend with explicit options.
func OpenAppendWith(path string, opts Options) (*Logger, io.Closer, error) {
	if path == "" {
		return nil, nil, nil
	}
	o := opts.normalized()
	expanded, err := ExpandPath(path)
	if err != nil {
		return nil, nil, err
	}
	writer, err := newRotatingWriter(expanded)
	if err != nil {
		return nil, nil, fmt.Errorf("open --log file: %w", err)
	}
	queue := newQueueWriter(writer, o)
	return newLogger(queue, o), queue, nil
}

// queueCapacity bounds how many lines may wait for the writer; the byte budget
// (Options.QueueSize) is the real limit, because one body line can be megabytes.
const queueCapacity = 1024

// logTarget is the rotated destination a queueWriter writes through. It is an
// interface so tests can inject a slow sink and prove writes never block.
type logTarget interface {
	io.Writer
	rotate(now time.Time, cleanup bool) error
	Close() error
	Name() string
}

// queueWriter is an io.Writer that hands lines to one goroutine. Writes never
// block: when the queue is full the line is dropped and counted, and a summary
// goes to stderr on an interval.
type queueWriter struct {
	target     logTarget
	ch         chan []byte
	rotateReq  chan time.Time
	done       chan struct{}
	bufSize    int
	queueSize  int
	flushEvery time.Duration
	warnf      func(format string, args ...any)

	mu     sync.RWMutex
	closed bool

	pending      atomic.Int64
	droppedLines atomic.Uint64
	droppedBytes atomic.Uint64

	wg   sync.WaitGroup
	once sync.Once
}

func newQueueWriter(target logTarget, opts Options) *queueWriter {
	w := &queueWriter{
		target:     target,
		ch:         make(chan []byte, queueCapacity),
		rotateReq:  make(chan time.Time),
		done:       make(chan struct{}),
		bufSize:    opts.BufferSize,
		queueSize:  opts.QueueSize,
		flushEvery: opts.FlushEvery,
		warnf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		},
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// Write queues data for the writer goroutine and never blocks on disk. The
// slice is owned by the writer from this point on; callers pass a freshly
// built line.
func (w *queueWriter) Write(data []byte) (int, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if len(data) == 0 {
		return 0, nil
	}
	// A single line larger than the budget is admitted when nothing else is
	// waiting, so --log-body=full can still record one huge body; memory stays
	// bounded by max(queue budget, one line).
	pending := w.pending.Add(int64(len(data)))
	if pending > int64(w.queueSize) && pending > int64(len(data)) {
		w.pending.Add(-int64(len(data)))
		w.drop(data)
		return len(data), nil
	}
	select {
	case w.ch <- data:
		return len(data), nil
	default:
		w.pending.Add(-int64(len(data)))
		w.drop(data)
		return len(data), nil
	}
}

func (w *queueWriter) drop(data []byte) {
	w.droppedLines.Add(1)
	w.droppedBytes.Add(uint64(len(data)))
}

// Dropped reports the lines and bytes dropped since the last successful write
// window. It exists for tests and for diagnostics.
func (w *queueWriter) Dropped() (lines, bytes uint64) {
	return w.droppedLines.Load(), w.droppedBytes.Load()
}

func (w *queueWriter) run() {
	defer w.wg.Done()
	buffer := bufio.NewWriterSize(w.target, w.bufSize)
	flush := time.NewTicker(w.flushEvery)
	defer flush.Stop()
	report := time.NewTicker(droppedReportEvery)
	defer report.Stop()
	rotate := time.NewTimer(time.Until(nextHour(time.Now())))
	defer rotate.Stop()

	for {
		select {
		case data, ok := <-w.ch:
			if !ok {
				w.drain(buffer)
				w.flush(buffer)
				w.reportDropped()
				return
			}
			w.write(buffer, data)
		case <-flush.C:
			w.flush(buffer)
		case <-report.C:
			w.reportDropped()
		case now := <-w.rotateReq:
			w.drain(buffer)
			w.flushAndRotate(buffer, now)
		case <-rotate.C:
			w.drain(buffer)
			w.flushAndRotate(buffer, time.Now())
			rotate.Reset(time.Until(nextHour(time.Now())))
		}
	}
}

func (w *queueWriter) write(buffer *bufio.Writer, data []byte) {
	if _, err := buffer.Write(data); err != nil {
		w.warnf("warning: write log: %v", err)
	}
	w.pending.Add(-int64(len(data)))
	if buffer.Buffered() >= w.bufSize*3/4 {
		w.flush(buffer)
	}
}

// drain moves every queued line into the buffer, so a rotation never splits a
// burst across two segments.
func (w *queueWriter) drain(buffer *bufio.Writer) {
	for {
		select {
		case data, ok := <-w.ch:
			if !ok {
				return
			}
			w.write(buffer, data)
		default:
			return
		}
	}
}

func (w *queueWriter) flush(buffer *bufio.Writer) {
	if buffer.Buffered() == 0 {
		return
	}
	if err := buffer.Flush(); err != nil {
		w.warnf("warning: flush log: %v", err)
	}
}

func (w *queueWriter) flushAndRotate(buffer *bufio.Writer, now time.Time) {
	w.flush(buffer)
	if err := w.target.rotate(now, true); err != nil {
		w.warnf("warning: rotate log %s: %v", w.target.Name(), err)
	}
}

func (w *queueWriter) reportDropped() {
	lines := w.droppedLines.Swap(0)
	if lines == 0 {
		return
	}
	bytes := w.droppedBytes.Swap(0)
	w.warnf("warning: log queue full, dropped %d lines (%s)", lines, FormatBytes(bytes))
}

// FormatBytes renders a byte count for notices and warnings.
func FormatBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// rotateNow asks the writer goroutine to flush and rotate, so a test can drive
// rotation without waiting for the hour boundary.
func (w *queueWriter) rotateNow(now time.Time) error {
	select {
	case w.rotateReq <- now:
		return nil
	case <-w.done:
		return os.ErrClosed
	}
}

// Close drains the queue, flushes the buffer, and closes the rotated target.
func (w *queueWriter) Close() error {
	var err error
	w.once.Do(func() {
		w.mu.Lock()
		w.closed = true
		close(w.done)
		close(w.ch)
		w.mu.Unlock()
		w.wg.Wait()
		err = w.target.Close()
	})
	return err
}

func nextHour(now time.Time) time.Time {
	return now.Truncate(time.Hour).Add(time.Hour)
}

// rotatingWriter appends to the current hourly segment under a process-shared
// flock, so concurrent proxy processes keep whole lines. Rotation is driven by
// queueWriter, not by a timer of its own.
type rotatingWriter struct {
	path     string
	lockFile *os.File

	mu      sync.Mutex
	file    *os.File
	hour    time.Time
	closed  bool
	closeMu sync.Once
}

func newRotatingWriter(path string) (*rotatingWriter, error) {
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	writer := &rotatingWriter{path: path, lockFile: lockFile}
	if err := writer.rotate(time.Now(), false); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return writer, nil
}

// Name is the configured log path, used in rotation warnings.
func (w *rotatingWriter) Name() string {
	return w.path
}

func (w *rotatingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if err := w.lock(); err != nil {
		return 0, err
	}
	defer w.unlock()
	return w.file.Write(data)
}

// rotate switches to now's hour and, when cleanup is true, removes segments
// older than the immediately preceding hour.
func (w *rotatingWriter) rotate(now time.Time, cleanup bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	if err := w.lock(); err != nil {
		return err
	}
	defer w.unlock()

	hour := now.Truncate(time.Hour)
	segment := segmentPath(w.path, hour)
	file, err := os.OpenFile(segment, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := replaceSymlink(w.path, filepath.Base(segment)); err != nil {
		_ = file.Close()
		return err
	}
	old := w.file
	w.file = file
	w.hour = hour
	if old != nil {
		_ = old.Close()
	}
	if cleanup {
		w.cleanup(hour)
	}
	return nil
}

func (w *rotatingWriter) cleanup(currentHour time.Time) {
	dir := filepath.Dir(w.path)
	base := filepath.Base(w.path) + "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: list expired logs in %s: %v\n", dir, err)
		return
	}
	cutoff := currentHour.Add(-2 * time.Hour)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), base) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), base)
		if len(suffix) != len(hourLayout) {
			continue
		}
		hour, err := time.ParseInLocation(hourLayout, suffix, currentHour.Location())
		if err != nil || hour.After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warning: remove expired log %s: %v\n", filepath.Join(dir, entry.Name()), err)
		}
	}
}

func (w *rotatingWriter) lock() error {
	return unix.Flock(int(w.lockFile.Fd()), unix.LOCK_EX)
}

func (w *rotatingWriter) unlock() {
	_ = unix.Flock(int(w.lockFile.Fd()), unix.LOCK_UN)
}

func (w *rotatingWriter) Close() error {
	var err error
	w.closeMu.Do(func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.closed = true
		if w.file != nil {
			err = w.file.Close()
		}
		if closeErr := w.lockFile.Close(); err == nil {
			err = closeErr
		}
	})
	return err
}

func segmentPath(path string, hour time.Time) string {
	return path + "." + hour.Format(hourLayout)
}

func replaceSymlink(path, target string) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symlink; move or remove the legacy log file before enabling rotation", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".llm-proxy-log-link-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Remove(tmpName); err != nil {
		return err
	}
	if err := os.Symlink(target, tmpName); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
