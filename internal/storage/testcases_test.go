package storage

import (
	"slices"
	"testing"
)

func id(v int64) *int64 { return &v }

// tree is root(1) with children a(2) and b(3); a1(4) sits under a.
func tree() []Folder {
	return []Folder{
		{ID: 1, Count: 1},
		{ID: 2, ParentID: id(1), Count: 2},
		{ID: 4, ParentID: id(2), Count: 3},
		{ID: 3, ParentID: id(1), Count: 5},
	}
}

func TestRollUpFolderTotals(t *testing.T) {
	f := tree()
	RollUpFolderTotals(f)
	want := map[int64]int{1: 11, 2: 5, 4: 3, 3: 5}
	for _, folder := range f {
		if folder.Total != want[folder.ID] {
			t.Errorf("folder %d total = %d, want %d", folder.ID, folder.Total, want[folder.ID])
		}
	}
}

func TestDescendantFolderIDs(t *testing.T) {
	got := DescendantFolderIDs(tree(), 2)
	slices.Sort(got)
	if !slices.Equal(got, []int64{2, 4}) {
		t.Fatalf("got %v", got)
	}
	all := DescendantFolderIDs(tree(), 1)
	if len(all) != 4 {
		t.Fatalf("root should include every folder, got %v", all)
	}
}

func TestEscapeLikeTreatsWildcardsLiterally(t *testing.T) {
	if got := escapeLike(`50%_off\`); got != `50\%\_off\\` {
		t.Fatalf("got %q", got)
	}
}
