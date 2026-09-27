// Package qmetry talks to QMetry Test Manager for Jira (QTM4J) Cloud's
// public Open API to read the TDMS block off a test case's custom field.
// Built against the same verified API shape as this workspace's
// QMetry/qtm4j_client.py, confirmed live against project ACP.
package qmetry

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	BaseURL      string
	JiraEmail    string
	JiraAPIToken string
	APIKey       string
	HTTPClient   *http.Client
}

func New(baseURL, jiraEmail, jiraAPIToken, apiKey string) *Client {
	return &Client{
		BaseURL:      baseURL,
		JiraEmail:    jiraEmail,
		JiraAPIToken: jiraAPIToken,
		APIKey:       apiKey,
		HTTPClient:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) authHeader() string {
	raw := c.JiraEmail + ":" + c.JiraAPIToken
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
}

func (c *Client) get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, query, nil)
}

func (c *Client) post(ctx context.Context, path string, query url.Values, reqBody any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, query, reqBody)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, reqBody any) ([]byte, error) {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.authHeader())
	req.Header.Set("apiKey", c.APIKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("qmetry API error %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

// CustomFieldValue is one entry of a test case's customFields map, as
// returned by GET /testcases/{id}/versions/{v}.
type CustomFieldValue struct {
	Name      string `json:"name"`
	FieldType int    `json:"fieldType"`
	Value     string `json:"value"`
}

// TestCaseVersion is the slice of a test case version's response TDMS
// actually needs: its human key (for the slot's identity) plus one
// custom field's value.
type TestCaseVersion struct {
	ID          string
	Key         string
	CustomField CustomFieldValue
}

type testCaseVersionResponse struct {
	Data struct {
		ID           string                      `json:"id"`
		Key          string                      `json:"key"`
		CustomFields map[string]CustomFieldValue `json:"customFields"`
	} `json:"data"`
}

// GetTestCaseVersion fetches one test case version and pulls out its key
// plus one custom field's value. Confirmed live: QMetry's Open API only
// populates customFields in the response when the `fields` query param
// names at least one field id — any field id unlocks the whole map,
// which is then filtered here to the one the caller asked for.
func (c *Client) GetTestCaseVersion(ctx context.Context, testCaseID string, versionNo int, fieldID string) (*TestCaseVersion, error) {
	path := fmt.Sprintf("/rest/api/latest/testcases/%s/versions/%d", url.PathEscape(testCaseID), versionNo)
	body, err := c.get(ctx, path, url.Values{"fields": {fieldID}})
	if err != nil {
		return nil, err
	}
	var parsed testCaseVersionResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decoding test case response: %w", err)
	}
	cf, ok := parsed.Data.CustomFields[fieldID]
	if !ok {
		return nil, fmt.Errorf("test case %s has no value for custom field %s", testCaseID, fieldID)
	}
	return &TestCaseVersion{ID: parsed.Data.ID, Key: parsed.Data.Key, CustomField: cf}, nil
}
