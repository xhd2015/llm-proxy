package logutil

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Logger struct {
	w io.Writer
}

func New(w io.Writer) *Logger {
	if w == nil {
		return nil
	}
	return &Logger{w: w}
}

// DefaultLogFile is the full-log destination used when --log is not passed.
const DefaultLogFile = "/tmp/llm-proxy.log"

// LogOff is the --log value that disables file logging entirely.
const LogOff = "off"

const hourLayout = "2006010215"

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

// OpenAppend opens an hourly-rotated log. The requested path is maintained as
// a symlink to the current hourly segment.
func OpenAppend(path string) (*Logger, io.Closer, error) {
	if path == "" {
		return nil, nil, nil
	}
	expanded, err := ExpandPath(path)
	if err != nil {
		return nil, nil, err
	}
	writer, err := newRotatingWriter(expanded, true)
	if err != nil {
		return nil, nil, fmt.Errorf("open --log file: %w", err)
	}
	return New(writer), writer, nil
}

func (l *Logger) Printf(format string, args ...any) {
	if l == nil || l.w == nil {
		return
	}
	line := fmt.Sprintf("%s ", time.Now().Format("2006/01/02 15:04:05")) + fmt.Sprintf(format, args...) + "\n"
	_, _ = io.WriteString(l.w, line)
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
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

type rotatingWriter struct {
	path     string
	lockFile *os.File

	mu      sync.Mutex
	file    *os.File
	hour    time.Time
	closed  bool
	done    chan struct{}
	wg      sync.WaitGroup
	closeMu sync.Once
}

func newRotatingWriter(path string, startWorker bool) (*rotatingWriter, error) {
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, err
	}
	writer := &rotatingWriter{path: path, lockFile: lockFile, done: make(chan struct{})}
	if err := writer.rotate(time.Now(), false); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	if startWorker {
		writer.wg.Add(1)
		go writer.rotateHourly()
	}
	return writer, nil
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

func (w *rotatingWriter) rotateHourly() {
	defer w.wg.Done()
	for {
		now := time.Now()
		nextHour := now.Truncate(time.Hour).Add(time.Hour)
		timer := time.NewTimer(time.Until(nextHour))
		select {
		case <-w.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			if err := w.rotate(time.Now(), true); err != nil {
				fmt.Fprintf(os.Stderr, "warning: rotate log %s: %v\n", w.path, err)
			}
		}
	}
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
		close(w.done)
		w.wg.Wait()
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
