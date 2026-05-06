package secrets

// CatalogEntry describes one provider that sunny knows how to wire
// secrets for. The entry is the source of truth for the TUI's secrets
// dialog, the daemon's `/secrets/catalog` endpoint, the onboarding
// flow, and any future mobile / web client.
//
// Fields a provider needs are listed in declaration order — that's
// the same order the TUI form renders. EnvVars is the matching
// environment variable name(s); env wins over file (see GetOrEnv).
//
// Why hardcoded: every provider has its own driver code in
// internal/provider/<name>/, so the catalog never grows past what
// sunny actually integrates with. A YAML config would be churn
// without value.
type CatalogEntry struct {
	Provider string         `json:"provider"`
	Label    string         `json:"label"`
	Fields   []CatalogField `json:"fields"`
	EnvVars  []string       `json:"env_vars,omitempty"`
	// HelpURL is a doc page the UI can link to ("how do I get an
	// Ollama Cloud key?"). Empty when no canonical page exists.
	HelpURL string `json:"help_url,omitempty"`
}

// CatalogField is one input on a provider's setup form.
type CatalogField struct {
	Key      string `json:"key"`
	Hint     string `json:"hint,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// catalog is the canonical list. Order matters — that's the order
// the secrets dialog and onboarding flow render the providers.
var catalog = []CatalogEntry{
	{
		Provider: "anthropic",
		Label:    "Anthropic API",
		Fields:   []CatalogField{{Key: "api_key", Hint: "sk-ant-…"}},
		EnvVars:  []string{"ANTHROPIC_API_KEY"},
		HelpURL:  "https://console.anthropic.com/settings/keys",
	},
	{
		Provider: "openai",
		Label:    "OpenAI API",
		Fields:   []CatalogField{{Key: "api_key", Hint: "sk-…"}},
		EnvVars:  []string{"OPENAI_API_KEY"},
		HelpURL:  "https://platform.openai.com/api-keys",
	},
	{
		Provider: "ollama",
		Label:    "Ollama Cloud",
		Fields: []CatalogField{
			{Key: "api_key", Hint: "ollama.com key"},
			{Key: "base_url", Hint: "https://ollama.com (default)", Optional: true},
		},
		EnvVars: []string{"OLLAMA_API_KEY"},
		HelpURL: "https://ollama.com/settings/keys",
	},
}

// Catalog returns a copy of the canonical provider catalog. Callers
// may freely mutate the returned slice; the underlying entries are
// shared but read-only by convention.
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}

// EntryFor returns the catalog entry for a provider, or nil if the
// provider isn't known.
func EntryFor(provider string) *CatalogEntry {
	for i := range catalog {
		if catalog[i].Provider == provider {
			cp := catalog[i]
			return &cp
		}
	}
	return nil
}
