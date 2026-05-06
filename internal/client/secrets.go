package client

import (
	"context"
	"encoding/json"
	"net/http"
)

// SecretInfo describes one provider's configured fields. Values are
// never carried over the wire — this is a status view, not a read.
type SecretInfo struct {
	Provider string   `json:"provider"`
	Fields   []string `json:"fields"`
}

// SecretCatalogEntry mirrors internal/secrets.CatalogEntry on the
// wire. Lives here so callers don't need to import the daemon-side
// secrets package.
type SecretCatalogEntry struct {
	Provider string                `json:"provider"`
	Label    string                `json:"label"`
	Fields   []SecretCatalogField  `json:"fields"`
	EnvVars  []string              `json:"env_vars,omitempty"`
	HelpURL  string                `json:"help_url,omitempty"`
}

// SecretCatalogField is one input on a provider's setup form.
type SecretCatalogField struct {
	Key      string `json:"key"`
	Hint     string `json:"hint,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// SecretsCatalog returns the canonical list of providers sunny knows
// how to wire. Pair with ListSecrets to know which entries are
// already filled in.
func (c *Client) SecretsCatalog(ctx context.Context) ([]SecretCatalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/secrets/catalog", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /secrets/catalog", resp)
	}
	var out []SecretCatalogEntry
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSecrets returns which providers have keys configured (no values).
func (c *Client) ListSecrets(ctx context.Context) ([]SecretInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/secrets", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errorFromBody("GET /secrets", resp)
	}
	var out []SecretInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutSecrets replaces all fields for a provider with the given map.
// Empty fields are dropped server-side.
func (c *Client) PutSecrets(ctx context.Context, provider string, fields map[string]string) error {
	resp, err := c.doJSON(ctx, http.MethodPut, "/secrets/"+provider, fields)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errorFromBody("PUT /secrets/"+provider, resp)
	}
	return nil
}

// DeleteSecrets removes a provider section. Idempotent.
func (c *Client) DeleteSecrets(ctx context.Context, provider string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/secrets/"+provider, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errorFromBody("DELETE /secrets/"+provider, resp)
	}
	return nil
}
