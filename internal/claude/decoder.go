package claude

import (
	"bufio"
	"encoding/json"
	"io"
)

// Decode reads line-delimited JSON events from r and emits them on the returned channel.
// The channel is closed when r reaches EOF or errors. Lines that fail to parse are
// surfaced as Event{Type: "parse_error", Result: <raw line>}.
func Decode(r io.Reader) <-chan Event {
	out := make(chan Event, 32)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(r)
		// stream-json lines can be large (tool_results with file contents)
		scanner.Buffer(make([]byte, 1<<16), 1<<24)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev Event
			if err := json.Unmarshal(line, &ev); err != nil {
				out <- Event{Type: "parse_error", Result: string(line)}
				continue
			}
			ev.Raw = append([]byte(nil), line...)
			out <- ev
		}
	}()
	return out
}
