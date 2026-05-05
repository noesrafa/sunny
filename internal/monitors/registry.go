package monitors

import (
	"context"
	"sort"
)

// Item is one event a Source produced this tick. ID drives
// deduplication across ticks; Fields is the free-form payload that
// rules match against and templating substitutes from
// (`${item.<field>}` resolves Fields[<field>]).
type Item struct {
	ID     string
	Fields map[string]any
}

// Source produces Items on demand. Implementations are stateless;
// per-monitor state (cursors, last-seen pointers) flows through the
// state argument and the returned newState replaces it.
type Source interface {
	Type() string
	Fetch(ctx context.Context, cfg map[string]any, state map[string]any) (items []Item, newState map[string]any, err error)
}

// Action does something with one matched Item. The vars argument
// carries results of previous actions in the same rule so
// `${dispatch.result}` substitutes correctly. Return value (any)
// becomes the action's contribution to vars under its Type() key.
type Action interface {
	Type() string
	Run(ctx context.Context, cfg map[string]any, item Item, vars map[string]any) (result any, err error)
}

// Registry holds every Source and Action wired at boot. Lookup is
// by Type() — names live in the YAML, so adding a new source type
// is one file with the implementation plus a Register call.
type Registry struct {
	sources map[string]Source
	actions map[string]Action
}

func NewRegistry() *Registry {
	return &Registry{
		sources: map[string]Source{},
		actions: map[string]Action{},
	}
}

func (r *Registry) RegisterSource(s Source) { r.sources[s.Type()] = s }
func (r *Registry) RegisterAction(a Action) { r.actions[a.Type()] = a }

func (r *Registry) Source(name string) (Source, bool) {
	s, ok := r.sources[name]
	return s, ok
}

func (r *Registry) Action(name string) (Action, bool) {
	a, ok := r.actions[name]
	return a, ok
}

// SourceTypes returns the registered source names alphabetically.
// Used by the meta-prompt primer so agents see the current options.
func (r *Registry) SourceTypes() []string {
	out := make([]string, 0, len(r.sources))
	for k := range r.sources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ActionTypes returns the registered action names alphabetically.
func (r *Registry) ActionTypes() []string {
	out := make([]string, 0, len(r.actions))
	for k := range r.actions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
