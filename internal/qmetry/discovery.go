package qmetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ResolveProject turns a Jira project key (e.g. "ACP") into QMetry's
// numeric project id, confirming the project is QMetry-enabled.
func (c *Client) ResolveProject(ctx context.Context, projectKey string) (int, error) {
	body, err := c.post(ctx, "/rest/api/latest/projects", nil,
		map[string]any{"search": projectKey, "qmetryEnabled": true})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Data []struct {
			ID  int    `json:"id"`
			Key string `json:"key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("decoding projects response: %w", err)
	}
	for _, p := range resp.Data {
		if p.Key == projectKey {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("no QMetry-enabled project with key %q", projectKey)
}

// FolderNode is one node of a project's test case folder tree.
type FolderNode struct {
	ID       int          `json:"id"`
	Name     string       `json:"name"`
	Children []FolderNode `json:"children"`
}

// FolderTree fetches a project's whole test case folder tree.
func (c *Client) FolderTree(ctx context.Context, projectID int) ([]FolderNode, error) {
	body, err := c.get(ctx, fmt.Sprintf("/rest/api/latest/projects/%d/testcase-folders", projectID), nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []FolderNode `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding folders response: %w", err)
	}
	return resp.Data, nil
}

// FindFolder walks a folder path like "Regression/Booking" (leading and
// trailing slashes ignored, segments matched case-insensitively) down the
// tree and returns its node.
func FindFolder(tree []FolderNode, folderPath string) (*FolderNode, error) {
	nodes := tree
	var matched *FolderNode
	var walked []string
	for _, segment := range strings.Split(folderPath, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		var next *FolderNode
		for i := range nodes {
			if strings.EqualFold(nodes[i].Name, segment) {
				next = &nodes[i]
				break
			}
		}
		if next == nil {
			var available []string
			for _, n := range nodes {
				available = append(available, n.Name)
			}
			where := strings.Join(walked, "/")
			if where == "" {
				where = "<root>"
			}
			return nil, fmt.Errorf("folder %q not found under %s (available: %s)", segment, where, strings.Join(available, ", "))
		}
		matched, nodes = next, next.Children
		walked = append(walked, next.Name)
	}
	if matched == nil {
		return nil, fmt.Errorf("folder path %q is empty", folderPath)
	}
	return matched, nil
}

// TestCaseRef is one test case found by search, with the raw value of the
// requested custom field (empty when the test case has no TDMS block).
type TestCaseRef struct {
	ID        string
	Key       string
	Summary   string
	Version   int
	FolderIDs []int
	RawBlock  string
}

const searchPageSize = 100

// SearchTestCases lists every test case in a project — or, when folderID is
// non-zero, in that folder and its subfolders — returning each one's latest
// version, summary, folders and the value of fieldID, all in one request per
// page. The body's "filter" wrapper is required: without it QMetry answers
// 403.
func (c *Client) SearchTestCases(ctx context.Context, projectID, folderID int, fieldID string) ([]TestCaseRef, error) {
	filter := map[string]any{"projectId": projectID}
	if folderID != 0 {
		filter["folderId"] = folderID
		filter["withChild"] = true
	}

	var refs []TestCaseRef
	for startAt := 0; ; startAt += searchPageSize {
		query := url.Values{
			"maxResults": {strconv.Itoa(searchPageSize)},
			"startAt":    {strconv.Itoa(startAt)},
			"fields":     {"key,summary,folders," + fieldID},
		}
		body, err := c.post(ctx, "/rest/api/latest/testcases/search/", query, map[string]any{"filter": filter})
		if err != nil {
			return nil, err
		}
		var page struct {
			Total int `json:"total"`
			Data  []struct {
				ID      string `json:"id"`
				Key     string `json:"key"`
				Summary string `json:"summary"`
				Version struct {
					VersionNo int `json:"versionNo"`
				} `json:"version"`
				Folders []struct {
					ID int `json:"id"`
				} `json:"folders"`
				CustomFields map[string]CustomFieldValue `json:"customFields"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("decoding search response: %w", err)
		}
		for _, tc := range page.Data {
			ref := TestCaseRef{
				ID:       tc.ID,
				Key:      tc.Key,
				Summary:  tc.Summary,
				Version:  tc.Version.VersionNo,
				RawBlock: tc.CustomFields[fieldID].Value,
			}
			for _, f := range tc.Folders {
				ref.FolderIDs = append(ref.FolderIDs, f.ID)
			}
			refs = append(refs, ref)
		}
		if len(page.Data) == 0 || startAt+len(page.Data) >= page.Total {
			return refs, nil
		}
	}
}
