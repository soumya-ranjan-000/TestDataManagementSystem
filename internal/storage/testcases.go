package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// Folder is one node of a team's mirrored folder subtree.
type Folder struct {
	ID       int64
	ParentID *int64
	Name     string
	Path     string
	Position int
	Depth    int
	// Count is the test cases filed directly in this folder; Total adds all
	// its subfolders.
	Count, Total int
}

// MirrorCase is one test case as synced from the test management app.
type MirrorCase struct {
	TCMTestCaseID string  `json:"tcm_test_case_id"`
	Key           string  `json:"test_case_key"`
	KeyNum        int     `json:"key_num"`
	Summary       string  `json:"summary"`
	Version       int     `json:"tcm_version"`
	FolderIDs     []int64 `json:"folder_ids"`
	RawBlock      string  `json:"raw_block"`
}

// mirrorChunk bounds one bulk upsert: large enough that a 4,000-case sync
// is a handful of statements, small enough to keep each one modest.
const mirrorChunk = 500

// ReplaceFolders swaps in the team's current folder subtree.
func (r *TeamRepo) ReplaceFolders(ctx context.Context, folders []Folder) error {
	type row struct {
		FolderID int64  `json:"folder_id"`
		ParentID *int64 `json:"parent_id"`
		Name     string `json:"name"`
		Path     string `json:"path"`
		Position int    `json:"position"`
		Depth    int    `json:"depth"`
	}
	rows := make([]row, len(folders))
	for i, f := range folders {
		rows[i] = row{f.ID, f.ParentID, f.Name, f.Path, f.Position, f.Depth}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM tcm_folders WHERE team_id = $1`, r.teamID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tcm_folders (team_id, folder_id, parent_id, name, path, position, depth)
		SELECT $1, x.folder_id, x.parent_id, x.name, x.path, x.position, x.depth
		FROM jsonb_to_recordset($2::jsonb) AS x(folder_id bigint, parent_id bigint, name text, path text, position int, depth int)`,
		r.teamID, payload); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MirrorTestCases upserts the synced test cases in bulk and un-removes any
// that came back.
func (r *TeamRepo) MirrorTestCases(ctx context.Context, cases []MirrorCase) error {
	for start := 0; start < len(cases); start += mirrorChunk {
		end := min(start+mirrorChunk, len(cases))
		chunk := cases[start:end]
		for i := range chunk {
			if chunk[i].FolderIDs == nil {
				chunk[i].FolderIDs = []int64{}
			}
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO test_cases (team_id, tcm_test_case_id, test_case_key, key_num, summary, tcm_version, folder_ids, raw_block)
			SELECT $1, x.tcm_test_case_id, x.test_case_key, x.key_num, x.summary, x.tcm_version, x.folder_ids, x.raw_block
			FROM jsonb_to_recordset($2::jsonb) AS x(tcm_test_case_id text, test_case_key text, key_num int,
			     summary text, tcm_version int, folder_ids bigint[], raw_block text)
			ON CONFLICT (team_id, test_case_key) DO UPDATE SET
				tcm_test_case_id = EXCLUDED.tcm_test_case_id, key_num = EXCLUDED.key_num,
				summary = EXCLUDED.summary, tcm_version = EXCLUDED.tcm_version,
				folder_ids = EXCLUDED.folder_ids, raw_block = EXCLUDED.raw_block,
				synced_at = now(), removed_at = NULL`,
			r.teamID, payload); err != nil {
			return fmt.Errorf("mirroring test cases %d-%d: %w", start, end, err)
		}
	}
	return nil
}

// MarkRemovedTestCases flags mirrored test cases missing from a complete
// listing as removed (they left the folder). Callers apply the same guard
// as slot orphaning: only a complete, non-empty, full-folder listing.
func (r *TeamRepo) MarkRemovedTestCases(ctx context.Context, present []string) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE test_cases SET removed_at = now()
		WHERE team_id = $1 AND removed_at IS NULL AND NOT (test_case_key = ANY($2))`,
		r.teamID, nonNilStrings(present))
	return tag.RowsAffected(), err
}

// CountTestCases is how many test cases the team's configured folder holds.
func (r *TeamRepo) CountTestCases(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM test_cases WHERE team_id = $1 AND removed_at IS NULL`, r.teamID).Scan(&n)
	return n, err
}

// Folders returns the team's folder subtree in display order, with direct
// and rolled-up test case counts.
func (r *TeamRepo) Folders(ctx context.Context) ([]Folder, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT f.folder_id, f.parent_id, f.name, f.path, f.position, f.depth,
		       (SELECT count(*) FROM test_cases t
		        WHERE t.team_id = f.team_id AND t.removed_at IS NULL AND t.folder_ids @> ARRAY[f.folder_id])
		FROM tcm_folders f WHERE f.team_id = $1 ORDER BY f.position`, r.teamID)
	if err != nil {
		return nil, err
	}
	folders, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Folder, error) {
		var f Folder
		err := row.Scan(&f.ID, &f.ParentID, &f.Name, &f.Path, &f.Position, &f.Depth, &f.Count)
		return f, err
	})
	if err != nil {
		return nil, err
	}
	RollUpFolderTotals(folders)
	return folders, nil
}

// RollUpFolderTotals sets each folder's Total to its own count plus all its
// descendants'. Folders must be in depth-first order.
func RollUpFolderTotals(folders []Folder) {
	index := map[int64]int{}
	for i := range folders {
		folders[i].Total = folders[i].Count
		index[folders[i].ID] = i
	}
	for i := len(folders) - 1; i >= 0; i-- {
		if p := folders[i].ParentID; p != nil {
			if pi, ok := index[*p]; ok {
				folders[pi].Total += folders[i].Total
			}
		}
	}
}

// DescendantFolderIDs returns id and every folder below it.
func DescendantFolderIDs(folders []Folder, id int64) []int64 {
	children := map[int64][]int64{}
	for _, f := range folders {
		if f.ParentID != nil {
			children[*f.ParentID] = append(children[*f.ParentID], f.ID)
		}
	}
	out := []int64{id}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}

// --- the paged test case table ---------------------------------------------

type TestCaseFilter string

const (
	FilterAll       TestCaseFilter = "all"
	FilterWithBlock TestCaseFilter = "with_block"
	FilterNoBlock   TestCaseFilter = "no_block"
	FilterAttention TestCaseFilter = "attention"
)

// filterSQL is a fixed set of clauses chosen by name — never user text.
var filterSQL = map[TestCaseFilter]string{
	FilterAll:       "TRUE",
	FilterWithBlock: "t.has_block",
	FilterNoBlock:   "NOT t.has_block",
	// Needs attention: carries a block but has no valid PNR, a broken block,
	// or a last check that didn't come back valid.
	FilterAttention: `t.has_block AND (s.id IS NULL OR i.id IS NULL OR s.block_error IS NOT NULL
		OR s.last_outcome IN ('invalid', 'check_error', 'generation_failed', 'block_error'))`,
}

func ValidFilter(f string) bool { _, ok := filterSQL[TestCaseFilter(f)]; return ok }

type TestCaseQuery struct {
	FolderIDs   []int64 // empty = every folder
	Search      string  // matches key or summary, case-insensitively
	Filter      TestCaseFilter
	Environment string // whose slot and test data to show
	Limit       int
	Offset      int
}

// TestCaseRow is one row of the table: a test case, and its slot and live
// test data in the chosen environment.
type TestCaseRow struct {
	ID            string
	Key           string
	Summary       string
	Version       int
	HasBlock      bool
	SlotID        *string
	Class         *string
	BlockError    *string
	LastCheckedAt *time.Time
	LastOutcome   *string
	OrphanedAt    *time.Time
	InstanceState *string
	TestData      []model.TestDatum
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// TestCasePage returns one page of the team's test cases matching q, and
// the total number of matches for the pager.
func (r *TeamRepo) TestCasePage(ctx context.Context, q TestCaseQuery) ([]TestCaseRow, int, error) {
	filter, ok := filterSQL[q.Filter]
	if !ok {
		filter = filterSQL[FilterAll]
	}
	from := `
		FROM test_cases t
		LEFT JOIN slots s ON s.team_id = t.team_id AND s.test_case_key = t.test_case_key AND s.environment = $2
		LEFT JOIN instances i ON i.slot_id = s.id AND i.state <> 'RETIRED'
		WHERE t.team_id = $1 AND t.removed_at IS NULL
		  AND (cardinality($3::bigint[]) = 0 OR t.folder_ids && $3::bigint[])
		  AND ($4 = '' OR t.test_case_key ILIKE $5 ESCAPE '\' OR t.summary ILIKE $5 ESCAPE '\')
		  AND (` + filter + `)`
	folderIDs := q.FolderIDs
	if folderIDs == nil {
		folderIDs = []int64{}
	}
	search := strings.TrimSpace(q.Search)
	args := []any{r.teamID, q.Environment, folderIDs, search, "%" + escapeLike(search) + "%"}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) `+from, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT t.id, t.test_case_key, t.summary, t.tcm_version, t.has_block,
		       s.id::text, s.class, s.block_error, s.last_checked_at, s.last_outcome, s.orphaned_at,
		       i.state, i.pnr, i.test_data `+from+`
		ORDER BY t.key_num, t.test_case_key
		LIMIT $6 OFFSET $7`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	page, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TestCaseRow, error) {
		var tc TestCaseRow
		var pnr *string
		var testData []byte
		err := row.Scan(&tc.ID, &tc.Key, &tc.Summary, &tc.Version, &tc.HasBlock,
			&tc.SlotID, &tc.Class, &tc.BlockError, &tc.LastCheckedAt, &tc.LastOutcome, &tc.OrphanedAt,
			&tc.InstanceState, &pnr, &testData)
		if err != nil {
			return tc, err
		}
		tc.TestData = decodeTestData(testData, pnr)
		return tc, nil
	})
	return page, total, err
}

// decodeTestData reads an instance's test data, falling back to the bare
// PNR for instances generated before test data was recorded.
func decodeTestData(raw []byte, pnr *string) []model.TestDatum {
	var data []model.TestDatum
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &data)
	}
	if len(data) == 0 && pnr != nil && *pnr != "" {
		data = []model.TestDatum{{PNR: *pnr}}
	}
	return data
}

// --- one test case's detail -------------------------------------------------

type InstanceRecord struct {
	PNR          string
	State        string
	RetireReason *string
	TestData     []model.TestDatum
	CreatedAt    time.Time
	RetiredAt    *time.Time
}

type SlotDetail struct {
	Environment   string
	Class         string
	BlockError    *string
	LastCheckedAt *time.Time
	LastOutcome   *string
	OrphanedAt    *time.Time
	Instances     []InstanceRecord // newest first; the first non-retired one is live
}

type TestCaseDetail struct {
	ID          string
	Key         string
	Summary     string
	Version     int
	RawBlock    string
	FolderPaths []string
	Slots       []SlotDetail
}

// instanceHistoryLimit caps the PNR history shown per slot.
const instanceHistoryLimit = 20

// TestCaseDetail returns one test case with its raw block and, for every
// environment, its slot and recent PNR history.
func (r *TeamRepo) TestCaseDetail(ctx context.Context, id string) (*TestCaseDetail, error) {
	var d TestCaseDetail
	err := r.pool.QueryRow(ctx, `
		SELECT t.id, t.test_case_key, t.summary, t.tcm_version, t.raw_block,
		       COALESCE((SELECT array_agg(f.path ORDER BY f.path) FROM tcm_folders f
		                 WHERE f.team_id = t.team_id AND f.folder_id = ANY(t.folder_ids)), '{}')
		FROM test_cases t WHERE t.id = $1 AND t.team_id = $2`, id, r.teamID,
	).Scan(&d.ID, &d.Key, &d.Summary, &d.Version, &d.RawBlock, &d.FolderPaths)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.environment, s.class, s.block_error, s.last_checked_at, s.last_outcome, s.orphaned_at
		FROM slots s WHERE s.team_id = $1 AND s.test_case_key = $2 ORDER BY s.environment`, r.teamID, d.Key)
	if err != nil {
		return nil, err
	}
	type slotRow struct {
		id string
		SlotDetail
	}
	slots, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (slotRow, error) {
		var s slotRow
		err := row.Scan(&s.id, &s.Environment, &s.Class, &s.BlockError, &s.LastCheckedAt, &s.LastOutcome, &s.OrphanedAt)
		return s, err
	})
	if err != nil {
		return nil, err
	}

	for _, s := range slots {
		rows, err := r.pool.Query(ctx, `
			SELECT COALESCE(i.pnr, ''), i.state, i.retire_reason, i.test_data, i.created_at, i.retired_at
			FROM instances i WHERE i.slot_id = $1
			ORDER BY i.created_at DESC LIMIT $2`, s.id, instanceHistoryLimit)
		if err != nil {
			return nil, err
		}
		s.Instances, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (InstanceRecord, error) {
			var ir InstanceRecord
			var testData []byte
			err := row.Scan(&ir.PNR, &ir.State, &ir.RetireReason, &testData, &ir.CreatedAt, &ir.RetiredAt)
			ir.TestData = decodeTestData(testData, &ir.PNR)
			return ir, err
		})
		if err != nil {
			return nil, err
		}
		d.Slots = append(d.Slots, s.SlotDetail)
	}
	return &d, nil
}

// UpdateTestData replaces a live instance's test data, e.g. with the
// passenger names read back from PSS during a check.
func (r *TeamRepo) UpdateTestData(ctx context.Context, instanceID string, data []model.TestDatum) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return r.expectOne(r.pool.Exec(ctx, `
		UPDATE instances SET test_data = $1
		FROM slots s WHERE instances.id = $2 AND s.id = instances.slot_id AND s.team_id = $3`,
		payload, instanceID, r.teamID))
}
