// Package secrets owns the daemon's API keys and provider config.
//
// Secrets live in `~/.sunny/secrets.yaml` (mode 0600), structured as
// provider name → field → value:
//
//	anthropic:
//	  api_key: sk-ant-…
//	openai:
//	  api_key: sk-…
//	ollama:
//	  api_key: …
//	  base_url: https://ollama.com
//
// Why structured YAML instead of a flat .env:
//   - Some providers carry more than a single string (Ollama: api_key
//     + base_url; future OAuth: refresh tokens; etc.).
//   - Namespacing is explicit instead of "OLLAMA_API_KEY"/"OPENAI_API_KEY"
//     prefix soup.
//   - Same family as agent.yaml — keeps the "filesystem is the truth"
//     story consistent.
//
// Env vars (e.g., ANTHROPIC_API_KEY) override the file when both are
// present. Useful for headless / CI / docker.
//
// The daemon never exposes secret values over HTTP. Reads happen
// in-process by provider drivers; the API only surfaces *which*
// fields are configured, never their content.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"gopkg.in/yaml.v3"
)

// FileName is the basename of the secrets file under the runtime root.
const FileName = "secrets.yaml"

// Path returns the absolute path of secrets.yaml for a given root.
func Path(root string) string { return filepath.Join(root, FileName) }

// Store is a thread-safe view over secrets.yaml. Reads hit the
// in-memory cache; writes flush to disk synchronously and atomically.
type Store struct {
	root string
	mu   sync.RWMutex
	data map[string]map[string]string
}

// New loads (or creates) the store for the given root. A missing file
// is not an error — first read returns "" for any field.
func New(root string) (*Store, error) {
	s := &Store{root: root, data: map[string]map[string]string{}}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// reload re-reads secrets.yaml into the in-memory map. Safe to call
// after external edits. Acquires the write lock.
func (s *Store) reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reloadLocked()
}

// reloadLocked re-reads secrets.yaml. Caller must hold s.mu (write).
// Used by mutators to merge concurrent CLI edits before write.
func (s *Store) reloadLocked() error {
	p := Path(s.root)
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.data = map[string]map[string]string{}
			return nil
		}
		return fmt.Errorf("read %s: %w", p, err)
	}
	var parsed map[string]map[string]string
	if len(raw) == 0 {
		parsed = map[string]map[string]string{}
	} else if err := yaml.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse %s: %w", p, err)
	}
	if parsed == nil {
		parsed = map[string]map[string]string{}
	}
	s.data = parsed
	return nil
}

// Get returns the field's stored value, or "" if absent. Does NOT
// consult env vars. For env-or-file lookups use GetOrEnv.
func (s *Store) Get(provider, field string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.data[provider]; ok {
		return p[field]
	}
	return ""
}

// GetOrEnv returns the env var if non-empty, else the stored value.
// Env wins so headless overrides work without touching the file.
func (s *Store) GetOrEnv(provider, field, envName string) string {
	if envName != "" {
		if v := os.Getenv(envName); v != "" {
			return v
		}
	}
	return s.Get(provider, field)
}

// SetProvider replaces ALL fields for a provider with the given map.
// Pass an empty map to clear the provider but keep its key visible
// (use Delete to remove the section entirely).
//
// Reloads from disk before writing so concurrent edits from the CLI
// (which has its own *Store) don't get stomped.
func (s *Store) SetProvider(provider string, fields map[string]string) error {
	if !validKey(provider) {
		return fmt.Errorf("invalid provider name %q", provider)
	}
	for k := range fields {
		if !validKey(k) {
			return fmt.Errorf("invalid field name %q", k)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	cleaned := map[string]string{}
	for k, v := range fields {
		if v == "" {
			continue
		}
		cleaned[k] = v
	}
	s.data[provider] = cleaned
	return s.flushLocked()
}

// SetField updates one field on a provider. Empty value clears that
// field (but keeps other fields intact). Use Delete to drop the
// provider entirely. Reloads from disk before writing.
func (s *Store) SetField(provider, field, value string) error {
	if !validKey(provider) || !validKey(field) {
		return fmt.Errorf("invalid provider/field name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if s.data[provider] == nil {
		s.data[provider] = map[string]string{}
	}
	if value == "" {
		delete(s.data[provider], field)
	} else {
		s.data[provider][field] = value
	}
	return s.flushLocked()
}

// Delete removes a provider section entirely. Idempotent. Reloads
// from disk before writing.
func (s *Store) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	delete(s.data, provider)
	return s.flushLocked()
}

// ProviderInfo describes which fields are configured for a provider,
// without revealing values. Used by GET /secrets and the TUI.
type ProviderInfo struct {
	Provider string   `json:"provider"`
	Fields   []string `json:"fields"`
}

// List returns all configured providers + their non-empty fields,
// sorted alphabetically. Values are NEVER included.
func (s *Store) List() []ProviderInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ProviderInfo, 0, len(s.data))
	for prov, fields := range s.data {
		fs := make([]string, 0, len(fields))
		for k, v := range fields {
			if v != "" {
				fs = append(fs, k)
			}
		}
		sort.Strings(fs)
		out = append(out, ProviderInfo{Provider: prov, Fields: fs})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// flushLocked serializes the in-memory map to disk. Caller must hold
// s.mu (write side). Atomic: write to .tmp, rename. Mode 0600 always.
func (s *Store) flushLocked() error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return err
	}
	cleaned := make(map[string]map[string]string, len(s.data))
	for prov, fields := range s.data {
		if len(fields) == 0 {
			continue
		}
		copy := make(map[string]string, len(fields))
		for k, v := range fields {
			if v != "" {
				copy[k] = v
			}
		}
		if len(copy) > 0 {
			cleaned[prov] = copy
		}
	}
	data, err := yaml.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("marshal secrets.yaml: %w", err)
	}
	p := Path(s.root)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write secrets.yaml: %w", err)
	}
	return os.Rename(tmp, p)
}

// validKey enforces a conservative shape on provider names + field
// names — lowercase ASCII alnum + underscore + dash. Keeps yaml clean
// and prevents path-shaped values.
func validKey(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= '0' && r <= '9',
			r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}
