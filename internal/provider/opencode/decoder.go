package opencode

import (
	"bufio"
	"encoding/json"
	"io"
)

// decode reads line-delimited JSON events from r and emits them on the
// returned channel. Closes when r reaches EOF or errors. Lines that
// fail to parse are surfaced as rawEvent{Type: "parse_error"} carrying
// the offending line in Raw, so callers can choose to log them rather
// than crash.
func decode(r io.Reader) <-chan rawEvent {
	out := make(chan rawEvent, 32)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		// Tool outputs (file contents, large grep results) can produce
		// big lines — bump the buffer well past Scanner's 64KB default.
		scanner.Buffer(make([]byte, 1<<16), 1<<24)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev rawEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				out <- rawEvent{Type: "parse_error", Raw: append(json.RawMessage(nil), line...)}
				continue
			}
			ev.Raw = append(json.RawMessage(nil), line...)
			out <- ev
		}
	}()
	return out
}
