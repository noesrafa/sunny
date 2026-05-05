package monitors

import (
	"fmt"
	"regexp"
	"strings"
)

// EvaluateWhen returns true if the condition matches the item.
// Condition shapes:
//
//	{text_matches: "<regex>"}       — regex on item.Fields["text"]
//	{all: [...subconditions...]}    — every sub must match
//	{any: [...subconditions...]}    — at least one must match
//
// Unknown keys evaluate to false. nil/empty cond evaluates to true
// so a rule with no `when` always fires (useful for "every item"
// patterns).
func EvaluateWhen(cond map[string]any, item Item) bool {
	if len(cond) == 0 {
		return true
	}
	if all, ok := asList(cond["all"]); ok {
		for _, sub := range all {
			m, ok := sub.(map[string]any)
			if !ok || !EvaluateWhen(m, item) {
				return false
			}
		}
		return true
	}
	if list, ok := asList(cond["any"]); ok {
		for _, sub := range list {
			m, ok := sub.(map[string]any)
			if ok && EvaluateWhen(m, item) {
				return true
			}
		}
		return false
	}
	if pat, ok := cond["text_matches"].(string); ok {
		text, _ := item.Fields["text"].(string)
		re, err := regexp.Compile(pat)
		if err != nil {
			return false
		}
		return re.MatchString(text)
	}
	return false
}

// asList accepts either []any (typical YAML) or []map[string]any
// (some unmarshalers).
func asList(v any) ([]any, bool) {
	if l, ok := v.([]any); ok {
		return l, true
	}
	if l, ok := v.([]map[string]any); ok {
		out := make([]any, len(l))
		for i, m := range l {
			out[i] = m
		}
		return out, true
	}
	return nil, false
}

// tmplRe matches `${ns.field}` placeholders. The `ns` is `item` or
// the Type() of a previously-run action in the same rule.
var tmplRe = regexp.MustCompile(`\$\{([a-zA-Z_][a-zA-Z0-9_]*)\.([a-zA-Z_][a-zA-Z0-9_]*)\}`)

// Substitute expands every ${ns.field} in s using item and the
// vars map. Unknown placeholders are left literal so the user sees
// a clear "didn't expand" signal in their dispatch prompt instead
// of an empty string.
//
// Special-case: when a vars[ns] entry is a scalar string, the
// `field == "result"` placeholder resolves to that scalar. This
// matches the common pattern where an action's return value is a
// plain string (dispatch returns the model's text).
func Substitute(s string, item Item, vars map[string]any) string {
	return tmplRe.ReplaceAllStringFunc(s, func(match string) string {
		groups := tmplRe.FindStringSubmatch(match)
		ns, field := groups[1], groups[2]
		switch ns {
		case "item":
			if v, ok := item.Fields[field]; ok {
				return fmt.Sprint(v)
			}
		default:
			v, ok := vars[ns]
			if !ok {
				return match
			}
			if rm, ok := v.(map[string]any); ok {
				if x, ok := rm[field]; ok {
					return fmt.Sprint(x)
				}
				return match
			}
			if field == "result" {
				return fmt.Sprint(v)
			}
		}
		return match
	})
}

// SubstituteMap applies Substitute to every string value (including
// nested maps and lists) in cfg. Returns a fresh map; cfg is left
// untouched. Used to expand placeholders in action config blocks
// before handing them to Action.Run.
func SubstituteMap(cfg map[string]any, item Item, vars map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = substituteValue(v, item, vars)
	}
	return out
}

func substituteValue(v any, item Item, vars map[string]any) any {
	switch t := v.(type) {
	case string:
		if !strings.Contains(t, "${") {
			return t
		}
		return Substitute(t, item, vars)
	case map[string]any:
		return SubstituteMap(t, item, vars)
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = substituteValue(x, item, vars)
		}
		return out
	default:
		return v
	}
}
