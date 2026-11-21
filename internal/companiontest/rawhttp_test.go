//go:build companion

package companiontest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// putConfigFile writes a whole config file through the companion's
// PUT /v1/config/file. hactl has no client method for that route — it had no
// product caller and was deleted with the permission policy's phase 2 (D-81)
// — but this tier still lays out its fixtures with it.
func putConfigFile(ctx context.Context, path, content string) error {
	q := url.Values{"path": {path}, "dry_run": {"false"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, compURL+"/v1/config/file?"+q.Encode(), strings.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+companionToken)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PUT /v1/config/file %s: HTTP %d: %s", path, resp.StatusCode, body)
	}
	return nil
}

// haAutomationConfig reads an automation's config out of Home Assistant's OWN
// config API, GET /api/config/automation/config/<id> — H-12's independent read
// path, so a write through hactl is never verified through hactl. No product
// command routes there (D-81 deleted the client method), so the tier asks HA
// directly.
func haAutomationConfig(ctx context.Context, id string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, haURL+"/api/config/automation/config/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+haToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/config/automation/config/%s: HTTP %d: %s", id, resp.StatusCode, body)
	}
	return body, nil
}
