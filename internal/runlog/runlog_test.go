package runlog

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

type memSink struct {
	mu      sync.Mutex
	batches [][]storage.LogEntry
	fail    bool
}

func (m *memSink) AppendRunLogs(_ context.Context, _ string, entries []storage.LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("db down")
	}
	m.batches = append(m.batches, entries)
	return nil
}

func (m *memSink) lines() []storage.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []storage.LogEntry
	for _, b := range m.batches {
		all = append(all, b...)
	}
	return all
}

func TestLoggerBatchesAndKeepsOrder(t *testing.T) {
	sink := &memSink{}
	l := New(sink, "run-1", nil)
	for i := 0; i < 250; i++ {
		l.Info("ACP-TC-1", "acp-dev", "line %d", i)
	}
	l.Close()
	lines := sink.lines()
	if len(lines) != 250 {
		t.Fatalf("got %d lines, want 250", len(lines))
	}
	for i, e := range lines {
		if e.Message != "line "+strconv.Itoa(i) {
			t.Fatalf("line %d out of order: %q", i, e.Message)
		}
	}
	if len(sink.batches) > 3 {
		t.Fatalf("expected batched writes, got %d batches for 250 lines", len(sink.batches))
	}
}

func TestLoggerFlushesPeriodicallyForLiveViewing(t *testing.T) {
	sink := &memSink{}
	l := New(sink, "run-1", nil)
	defer l.Close()
	l.Warn("", "", "slow step")
	deadline := time.Now().Add(flushInterval + 2*time.Second)
	for time.Now().Before(deadline) {
		if len(sink.lines()) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("a single line was never flushed without Close")
}

func TestLoggerNeverPanicsWhenStorageFails(t *testing.T) {
	sink := &memSink{fail: true}
	l := New(sink, "run-1", nil)
	l.Error("", "", "boom")
	l.Close()
	l.Close() // idempotent
	var nilLogger *Logger
	nilLogger.Info("", "", "a nil logger is a no-op")
}

func TestEchoAndFormat(t *testing.T) {
	var out bytes.Buffer
	l := New(&memSink{}, "run-1", &out)
	l.Error("ACP-TC-2", "acp-dev", "PSS said %d", 503)
	l.Close()
	got := out.String()
	for _, want := range []string{"ERROR", "ACP-TC-2 acp-dev", "PSS said 503"} {
		if !strings.Contains(got, want) {
			t.Errorf("echo %q missing %q", got, want)
		}
	}
}
