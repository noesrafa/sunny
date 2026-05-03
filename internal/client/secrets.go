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
