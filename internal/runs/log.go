package runs

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LogLine is one line emitted on a run's stdout or stderr,
// timestamped at write time. Stream is "out" or "err".
type LogLine struct {
	Seq    uint64    `json:"seq"`
	Time   time.Time `json:"time"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

// logSink writes a run's stdout/stderr to disk and broadcasts each
// new line to live watchers. The supervisor opens one per started
// run and closes it on process exit.
type logSink struct {
	file *os.File

	mu   sync.Mutex
	subs map[chan LogLine]struct{}
	seq  uint64
}

func newLogSink(path string) (*logSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &logSink{file: f, subs: map[chan LogLine]struct{}{}}, nil
}

// Write records one line and fans it out to subscribers. The on-disk
// format is "<rfc3339nano> [stream] text\n" so the file is greppable
// and parseable by TailLog. Slow subscribers drop lines rather than
// block the writer.
func (s *logSink) Write(stream, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	line := LogLine{Seq: s.seq, Time: time.Now().UTC(), Stream: stream, Text: text}
	fmt.Fprintf(s.file, "%s [%s] %s\n", line.Time.Format(time.RFC3339Nano), stream, text)
	for sub := range s.subs {
		select {
		case sub <- line:
		default:
		}
	}
}

// Subscribe returns a channel of future log lines and a release
// func. Always defer the release; an unreleased subscription is a
// leaked channel slot. Lines published before Subscribe are NOT
// replayed — pair with TailLog for catch-up.
func (s *logSink) Subscribe() (<-chan LogLine, func()) {
	ch := make(chan LogLine, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func (s *logSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		delete(s.subs, ch)
		close(ch)
	}
	return s.file.Close()
}

// TailLog reads the last n lines from path and parses them back
// into LogLines. n <= 0 returns all lines. Missing file → nil
// slice, nil error. Malformed lines come back with Stream="" and
// Text=raw content so the caller still sees them.
func TailLog(path string, n int) ([]LogLine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil, nil
	}
	lines := strings.Split(trimmed, "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]LogLine, 0, len(lines))
	for i, raw := range lines {
		out = append(out, parseLogLine(raw, uint64(i+1)))
	}
	return out, nil
}

// parseLogLine reverses the on-disk format. Returns a LogLine with
// the parsed timestamp + stream when possible, or a Text-only line
// when the prefix is malformed.
func parseLogLine(raw string, seq uint64) LogLine {
	parts := strings.SplitN(raw, " ", 3)
	if len(parts) < 3 {
		return LogLine{Seq: seq, Text: raw}
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return LogLine{Seq: seq, Text: raw}
	}
	stream := strings.TrimSuffix(strings.TrimPrefix(parts[1], "["), "]")
	return LogLine{Seq: seq, Time: t, Stream: stream, Text: parts[2]}
}

// pumpLog reads lines from r and forwards each to sink under the
// given stream label. Closes r on return. Used for both stdout and
// stderr pipes of a managed run.
func pumpLog(r io.ReadCloser, stream string, sink *logSink) {
	defer r.Close()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		sink.Write(stream, sc.Text())
	}
}
