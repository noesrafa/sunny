package opencode

import (
	"encoding/json"
	"strings"
)

// mapEffort translates sunny's effort scale to opencode's --variant.
// opencode treats variants as provider-specific reasoning levels;
// the recognized labels pass through, and any provider that doesn't
// recognize them simply ignores the flag.
func mapEffort(eff string) string {
	v := strings.ToLower(strings.TrimSpace(eff))
	switch v {
	case "":
		return ""
	case "low", "medium", "high", "max", "minimal":
		return v
	case "xhigh":
		return "max"
	}
	return ""
}

// summarizeToolOutput renders the tool's terminal state into the
// (content, isError) pair sunny shows in transcripts. opencode's
// state shape varies per tool so we accept several output forms and
// fall back to raw JSON.
func summarizeToolOutput(s toolState) (string, bool) {
	isErr := s.Status == "error" || s.Error != ""
	if s.Error != "" {
		return s.Error, true
	}
	if len(s.Output) == 0 {
		if s.Title != "" {
			return s.Title, isErr
		}
		return "", isErr
	}
	// Most tools emit a plain string.
	var str string
	if err := json.Unmarshal(s.Output, &str); err == nil {
		return str, isErr
	}
	// Some emit {output: "...", metadata: {...}}.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(s.Output, &obj); err == nil {
		if raw, ok := obj["output"]; ok {
			var inner string
			if err := json.Unmarshal(raw, &inner); err == nil {
				return inner, isErr
			}
		}
	}
	return string(s.Output), isErr
}

// decodeError extracts a human-readable string from opencode's
// {name, data:{message}, ...} error payloads. Falls back to the raw
// JSON when the shape is unfamiliar.
func decodeError(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Data.Message != "" {
			return obj.Data.Message
		}
		if obj.Name != "" {
			return obj.Name
		}
	}
	return string(raw)
}
