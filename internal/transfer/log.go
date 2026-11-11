package transfer

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger is the import/export progress log. Lines go to the console writers
// and, optionally, to a file; the file keeps the complete record (per row),
// the console keeps operator-relevant milestones.
type Logger struct {
	mu      sync.Mutex
	console io.Writer
	file    io.Writer
}

// NewLogger builds a logger writing to console (nil discards) plus an
// optional file path ("" = no file). Console and file share one text format.
func NewLogger(console io.Writer, filePath string) (*Logger, error) {
	if console == nil {
		console = io.Discard
	}
	l := &Logger{console: console}
	if filePath != "" {
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open transfer log %s: %w", filePath, err)
		}
		l.file = f
	}
	return l, nil
}

// NewDiscardLogger discards everything.
func NewDiscardLogger() *Logger { return &Logger{console: io.Discard} }

// Close closes the log file, if any.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if closer, ok := l.file.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (l *Logger) writef(level, format string, args ...any) {
	if l == nil {
		return
	}
	line := fmt.Sprintf(time.Now().UTC().Format("2006-01-02T15:04:05Z")+" %-5s "+format, append([]any{level}, args...)...)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.console, line+"\n")
	if l.file != nil {
		_, _ = io.WriteString(l.file, line+"\n")
	}
}

func (l *Logger) Infof(format string, args ...any)   { l.writef("INFO", format, args...) }
func (l *Logger) Warnf(format string, args ...any)   { l.writef("WARN", format, args...) }
func (l *Logger) Warn(v ...any)                      { l.writef("WARN", "%s", fmt.Sprint(v...)) }
func (l *Logger) Noticef(format string, args ...any) { l.writef("NOTE", format, args...) }
