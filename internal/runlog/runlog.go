// Package runlog records a run's step-by-step activity log. Lines are
// buffered and written in batches — a round trip per line to a remote
// database would slow a large run badly — and flushed at least every couple
// of seconds so a running scan's log can be watched live. Logging never
// fails a run: a write error is reported once and the run carries on.
package runlog

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

const (
	batchSize     = 100
	flushInterval = 2 * time.Second
)

// Sink stores batches of log lines for one run.
type Sink interface {
	AppendRunLogs(ctx context.Context, runID string, entries []storage.LogEntry) error
}

type Logger struct {
	sink  Sink
	runID string
	echo  io.Writer // optional: also print each line (the CLI uses stdout)

	mu        sync.Mutex
	flushMu   sync.Mutex // one write at a time, so batches land in log order
	buf       []storage.LogEntry
	failed    bool // a write already failed; warned once
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

// New starts a logger for one run. Call Close to flush what's left.
func New(sink Sink, runID string, echo io.Writer) *Logger {
	l := &Logger{sink: sink, runID: runID, echo: echo, stop: make(chan struct{}), done: make(chan struct{})}
	go l.flushLoop()
	return l
}

func (l *Logger) flushLoop() {
	defer close(l.done)
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.Flush()
		}
	}
}

// Info, Warn and Error log one line. testCaseKey and environment may be
// empty for run-level lines.
func (l *Logger) Info(testCaseKey, environment, format string, args ...any) {
	l.log(storage.LogInfo, testCaseKey, environment, format, args...)
}

func (l *Logger) Warn(testCaseKey, environment, format string, args ...any) {
	l.log(storage.LogWarn, testCaseKey, environment, format, args...)
}

func (l *Logger) Error(testCaseKey, environment, format string, args ...any) {
	l.log(storage.LogError, testCaseKey, environment, format, args...)
}

func (l *Logger) log(level storage.LogLevel, key, env, format string, args ...any) {
	if l == nil {
		return
	}
	e := storage.LogEntry{At: time.Now().UTC(), Level: level, TestCaseKey: key, Environment: env,
		Message: fmt.Sprintf(format, args...)}
	if l.echo != nil {
		fmt.Fprintln(l.echo, Format(e, time.UTC))
	}
	l.mu.Lock()
	l.buf = append(l.buf, e)
	full := len(l.buf) >= batchSize
	l.mu.Unlock()
	if full {
		l.Flush()
	}
}

// Flush writes buffered lines now.
func (l *Logger) Flush() {
	if l == nil {
		return
	}
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.mu.Lock()
	batch := l.buf
	l.buf = nil
	l.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := l.sink.AppendRunLogs(ctx, l.runID, batch); err != nil {
		l.mu.Lock()
		first := !l.failed
		l.failed = true
		l.mu.Unlock()
		if first {
			log.Printf("runlog: writing log for run %s failed (further failures not reported): %v", l.runID, err)
		}
	}
}

// Close stops the periodic flush and writes whatever is left.
func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.closeOnce.Do(func() {
		close(l.stop)
		<-l.done
		l.Flush()
	})
}

// Format renders one line as plain text, for the CLI and log downloads.
func Format(e storage.LogEntry, loc *time.Location) string {
	where := e.TestCaseKey
	if e.Environment != "" {
		if where != "" {
			where += " "
		}
		where += e.Environment
	}
	if where == "" {
		where = "-"
	}
	return fmt.Sprintf("%s  %-5s  %-20s  %s", e.At.In(loc).Format("2006-01-02 15:04:05.000 MST"),
		upper(string(e.Level)), where, e.Message)
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
