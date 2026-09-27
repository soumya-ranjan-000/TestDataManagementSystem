package scan

import (
	"context"
	"fmt"
	"strings"

	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

// TestCase is one test case as the test management app reports it, with
// the raw TDMS block (empty when the test case doesn't carry one).
type TestCase struct {
	ID        string
	Key       string
	Summary   string
	Version   int
	FolderIDs []int
	RawBlock  string
}

// Listing is everything a sync reads from the test management app: the
// team's folder subtree and every test case in it.
type Listing struct {
	Folders []storage.Folder
	Cases   []TestCase
}

// Source is a test management app a team's test cases live in. QMetry is
// the only one today; another app plugs in by implementing this and being
// added to NewSource.
type Source interface {
	List(ctx context.Context) (*Listing, error)
}

// NewSource builds the Source a team's settings point at.
func NewSource(team *storage.Team, qmetryClient *qmetry.Client) (Source, error) {
	if team.TCMCustomFieldID == "" {
		return nil, fmt.Errorf("team %q has no TDMS custom field configured", team.Name)
	}
	switch team.TCMProvider {
	case "qmetry":
		return &qmetrySource{client: qmetryClient, projectKey: team.TCMProjectKey,
			folderPath: team.TCMFolderPath, fieldID: team.TCMCustomFieldID}, nil
	default:
		return nil, fmt.Errorf("unsupported test management app %q", team.TCMProvider)
	}
}

type qmetrySource struct {
	client     *qmetry.Client
	projectKey string
	folderPath string
	fieldID    string
}

func (s *qmetrySource) List(ctx context.Context) (*Listing, error) {
	projectID, err := s.client.ResolveProject(ctx, s.projectKey)
	if err != nil {
		return nil, err
	}
	tree, err := s.client.FolderTree(ctx, projectID)
	if err != nil {
		return nil, err
	}

	listing := &Listing{}
	folderID := 0
	if strings.Trim(s.folderPath, "/ ") != "" {
		root, err := qmetry.FindFolder(tree, s.folderPath)
		if err != nil {
			return nil, err
		}
		folderID = root.ID
		listing.Folders = FlattenFolders([]qmetry.FolderNode{*root})
	} else {
		listing.Folders = FlattenFolders(tree)
	}

	refs, err := s.client.SearchTestCases(ctx, projectID, folderID, s.fieldID)
	if err != nil {
		return nil, err
	}
	listing.Cases = make([]TestCase, len(refs))
	for i, r := range refs {
		listing.Cases[i] = TestCase{ID: r.ID, Key: r.Key, Summary: r.Summary, Version: r.Version,
			FolderIDs: r.FolderIDs, RawBlock: r.RawBlock}
	}
	return listing, nil
}

// FlattenFolders turns folder trees into a depth-first list with parent
// links and slash-joined paths; the given nodes become roots.
func FlattenFolders(roots []qmetry.FolderNode) []storage.Folder {
	var out []storage.Folder
	var walk func(nodes []qmetry.FolderNode, parent *int64, prefix string, depth int)
	walk = func(nodes []qmetry.FolderNode, parent *int64, prefix string, depth int) {
		for _, n := range nodes {
			id := int64(n.ID)
			path := n.Name
			if prefix != "" {
				path = prefix + "/" + n.Name
			}
			out = append(out, storage.Folder{ID: id, ParentID: parent, Name: n.Name, Path: path,
				Position: len(out), Depth: depth})
			walk(n.Children, &id, path, depth+1)
		}
	}
	walk(roots, nil, "", 0)
	return out
}
