package server

import "net/http"

// providerEntry is one selectable (provider, model) pair surfaced to
// clients. The label is the user-facing string the UI renders; the
// (provider, model) pair is what gets written to agent.yaml.
type providerEntry struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Label    string `json:"label"`
}

// catalog is the hardcoded set of provider+model pairs sunny knows
// how to route. Keep this list short and curated — adding entries
// here is the supported way to expose new options in the UI. The
// actual response is filtered by which providers the engine has
// successfully built (so e.g. opencode entries vanish if the CLI
// isn't installed).
var catalog = []providerEntry{
	{Provider: "claude-code", Model: "claude-opus-4-7", Label: "Claude Code · Opus 4.7"},
	{Provider: "claude-code", Model: "claude-sonnet-4-6", Label: "Claude Code · Sonnet 4.6"},
	{Provider: "claude-code", Model: "claude-haiku-4-5", Label: "Claude Code · Haiku 4.5"},
	{Provider: "ollama", Model: "gemma3:27b", Label: "Ollama · Gemma 3 27B"},
	{Provider: "opencode", Model: "gpt-5", Label: "OpenCode · GPT-5"},
}

// listProviders answers GET /providers with the catalog filtered by
// providers the daemon has successfully configured. Empty list when
// no providers are up — the UI uses that as a "go set up secrets"
// signal.
func (s *server) listProviders(w http.ResponseWriter, _ *http.Request) {
	available := map[string]bool{}
	if eng := s.engine.Load(); eng != nil {
		for _, name := range eng.ProviderNames() {
			available[name] = true
		}
	}
	out := make([]providerEntry, 0, len(catalog))
	for _, e := range catalog {
		if available[e.Provider] {
			out = append(out, e)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
