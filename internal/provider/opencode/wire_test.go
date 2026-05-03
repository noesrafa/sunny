package opencode

import (
	"encoding/json"
	"testing"
)

func TestMapEffort(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"low":      "low",
		"MEDIUM":   "medium",
		"  high  ": "high",
		"max":      "max",
		"minimal":  "minimal",
		"xhigh":    "max",
		"bogus":    "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := mapEffort(in); got != want {
				t.Errorf("mapEffort(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestSummarizeToolOutput_PlainString(t *testing.T) {
	s := toolState{Status: "completed", Output: json.RawMessage(`"hello world"`)}
	got, isErr := summarizeToolOutput(s)
	if got != "hello world" || isErr {
		t.Errorf("got (%q, %v), want (%q, false)", got, isErr, "hello world")
	}
}

func TestSummarizeToolOutput_NestedOutputField(t *testing.T) {
	s := toolState{
		Status: "completed",
		Output: json.RawMessage(`{"output":"line1\nline2","metadata":{"x":1}}`),
	}
	got, isErr := summarizeToolOutput(s)
	if got != "line1\nline2" || isErr {
		t.Errorf("got (%q, %v), want (%q, false)", got, isErr, "line1\nline2")
	}
}

func TestSummarizeToolOutput_ErrorStatus(t *testing.T) {
	s := toolState{Status: "error", Error: "permission denied"}
	got, isErr := summarizeToolOutput(s)
	if got != "permission denied" || !isErr {
		t.Errorf("got (%q, %v), want (%q, true)", got, isErr, "permission denied")
	}
}

func TestSummarizeToolOutput_FallsBackToRawJSON(t *testing.T) {
	// Object without "output" key — driver returns raw JSON so the
	// user at least sees something diagnosable instead of "".
	s := toolState{Status: "completed", Output: json.RawMessage(`{"foo":"bar"}`)}
	got, isErr := summarizeToolOutput(s)
	if got != `{"foo":"bar"}` || isErr {
		t.Errorf("got (%q, %v), want raw JSON, false", got, isErr)
	}
}

func TestDecodeError_PlainString(t *testing.T) {
	if got := decodeError(json.RawMessage(`"boom"`)); got != "boom" {
		t.Errorf("got %q, want %q", got, "boom")
	}
}

func TestDecodeError_NestedDataMessage(t *testing.T) {
	raw := json.RawMessage(`{"name":"AuthError","data":{"message":"401 Unauthorized"}}`)
	if got := decodeError(raw); got != "401 Unauthorized" {
		t.Errorf("got %q, want %q", got, "401 Unauthorized")
	}
}

func TestDecodeError_FallsBackToName(t *testing.T) {
	raw := json.RawMessage(`{"name":"WeirdError"}`)
	if got := decodeError(raw); got != "WeirdError" {
		t.Errorf("got %q, want %q", got, "WeirdError")
	}
}

func TestDecodeError_Empty(t *testing.T) {
	if got := decodeError(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
