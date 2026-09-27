package web

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/model"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

func sp(s string) *string { return &s }

// TestEveryPageRenders executes each template with representative data, so
// a template error (wrong field, wrong func argument type) fails a test
// instead of a user's page.
func TestEveryPageRenders(t *testing.T) {
	pages, err := parsePages()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{pages: pages}

	now := time.Now()
	user := &storage.User{ID: "u1", Email: "sree@example.com", Role: storage.RoleAdmin}
	team := &storage.Team{Name: "booking-squad", TCMProjectKey: "ACP", TCMCustomFieldID: "qcf_7932330",
		TCMFolderPath: "/chatbot-booking-retrival", RunTimes: []string{"02:00"}, Timezone: "Asia/Dubai"}
	run := &storage.Run{ID: "11111111-2222-3333-4444-555555555555", Trigger: "manual", Mode: storage.ModeHeal,
		Status: storage.RunRunning, CreatedAt: now, SyncError: sp("qmetry down"),
		Counts: storage.Counts{Total: 2, Valid: 1, Generated: 1, Errors: 1}}
	items := []storage.RunItem{
		{TestCaseKey: "ACP-TC-1", Environment: "acp-dev", Outcome: storage.OutcomeRegenerated,
			OldPNR: sp("US2UT2"), NewPNR: sp("ANJG2V"), FailedRule: sp("booking_status")},
		{TestCaseKey: "ACP-TC-2", Environment: "acp-dev", Outcome: storage.OutcomeCheckError, Error: sp("timeout <script>")},
	}
	parent := int64(1)
	tcData := testCasesData{
		State: tcState{Folder: "2", Q: "chatbot", Filter: "all", Env: "acp-dev", Page: 2, Size: 10},
		Rows: []storage.TestCaseRow{
			{ID: "11111111-0000-0000-0000-000000000001", Key: "ACP-TC-1", Summary: "Retrieves booking <b>", Version: 1,
				HasBlock: true, SlotID: sp("s1"), InstanceState: sp("VALID"), LastOutcome: sp("valid"), LastCheckedAt: &now,
				TestData: []model.TestDatum{{PNR: "ANJG2V", LastName: "AdtA"}}},
			{ID: "11111111-0000-0000-0000-000000000002", Key: "ACP-TC-2", Version: 1, HasBlock: true, SlotID: sp("s2"),
				BlockError: sp("bad yaml")},
			{ID: "11111111-0000-0000-0000-000000000003", Key: "ACP-TC-3", Version: 1},
		},
		Folders: []folderLink{
			{Folder: storage.Folder{ID: 1, Name: "root", Total: 6}, URL: "/testcases?folder=1"},
			{Folder: storage.Folder{ID: 2, ParentID: &parent, Name: "child", Depth: 1, Total: 2}, URL: "/testcases?folder=2", Active: true},
		},
		AllURL: "/testcases", AllCount: 6, Environments: []storage.Environment{{Name: "acp-dev"}},
		Filters: filterOptions, Sizes: pageSizes, Total: 25, Page: 2, Pages: 3, From: 11, To: 20,
		FirstURL: "/testcases?page=1", PrevURL: "/testcases?page=1", NextURL: "/testcases?page=3", LastURL: "/testcases?page=3",
	}

	cases := map[string]any{
		"login":     "sree@example.com",
		"password":  nil,
		"dashboard": dashboardData{Stats: []storage.EnvStats{{Environment: "acp-dev", Valid: 2}}, Runs: []storage.Run{*run}, Problems: items, NextRun: &now},
		"testcases": tcData,
		"scan_new":  newScanData{Environments: []storage.Environment{{Name: "acp-dev"}}},
		"runs":      []storage.Run{*run},
		"run": &runData{Run: run, Items: items, Live: true, Log: runLogData{
			Loc: time.UTC, RunID: run.ID, Live: true, Follow: true, Filter: "all", Size: 100,
			Sizes: logPageSizes, Filters: logFilterOptions, Total: 3, Page: 1, Pages: 1, From: 1, To: 3,
			SelfURL: "/runs/" + run.ID + "/log?page=last",
			Entries: []storage.LogEntry{
				{At: now, Level: storage.LogInfo, Message: "Run started: mode heal"},
				{At: now, Level: storage.LogWarn, TestCaseKey: "ACP-TC-1", Environment: "acp-dev",
					Message: "PNR US2UT2 is invalid: status REFUNDED <script>"},
				{At: now, Level: storage.LogError, TestCaseKey: "ACP-TC-2", Message: "generation_failed: boom"},
			}}},
		"report": reportData{From: "2026-09-01", To: "2026-09-27", Instances: []storage.GeneratedInstance{
			{TestCaseKey: "ACP-TC-1", Environment: "acp-dev", PNR: "US2UT2", State: "RETIRED",
				RetireReason: sp("consumed"), RunID: sp(run.ID), CreatedAt: now, RetiredAt: &now}}},
		"settings": settingsData{Available: []storage.Environment{{ID: "e1", Name: "acp-dev", PSSBaseURL: "https://pss"}},
			Enrolled: map[string]bool{"e1": true}, RunTimes: "02:00",
			Recipients: []storage.Recipient{{ID: "r1", Email: "qa@example.com", OnScan: true}}},
		"members": membersData{Members: []storage.User{*user, {ID: "u2", Email: "b@example.com", Role: "member", MustChangePassword: true}},
			TempPasswordFor: "b@example.com", TempPassword: "abcDEF234"},
	}
	for name := range pages {
		if _, ok := cases[name]; !ok {
			t.Errorf("page %q has no render test case", name)
		}
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.render(rec, httptest.NewRequest("GET", "/", nil), 200, name,
				page{Title: name, User: user, Team: team, Data: data})
			body := rec.Body.String()
			if !strings.Contains(body, "</html>") {
				t.Fatalf("page did not render completely:\n%s", body)
			}
			if strings.Contains(body, "<script>") && !strings.Contains(body, "&lt;script&gt;") && name == "run" {
				t.Fatal("error text was not escaped")
			}
		})
	}

	t.Run("live fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "run", "live", page{Data: &runData{Run: run, Items: items, Live: true}})
		if !strings.Contains(rec.Body.String(), `hx-trigger="every 2s"`) {
			t.Fatalf("live fragment missing polling: %s", rec.Body.String())
		}
	})
	t.Run("results fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "testcases", "results", page{Data: tcData})
		body := rec.Body.String()
		for _, want := range []string{"11–20 of 25", "Page 2 of 3", "ANJG2V", "AdtA", "NO TDMS BLOCK", "BLOCK ERROR",
			`<option value="10" selected>`, "Retrieves booking &lt;b&gt;"} {
			if !strings.Contains(body, want) {
				t.Errorf("results fragment missing %q", want)
			}
		}
		if strings.Contains(body, "tc-folders") {
			t.Error("results fragment should not re-render the folder tree")
		}
	})
	t.Run("browser fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "testcases", "browser", page{Data: tcData})
		body := rec.Body.String()
		if !strings.Contains(body, `folder depth-1 active`) || !strings.Contains(body, `value="chatbot"`) {
			t.Errorf("browser fragment missing active folder or search value: %s", body)
		}
	})
	t.Run("detail fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		d := &storage.TestCaseDetail{Key: "ACP-TC-1", Version: 1, FolderPaths: []string{"chatbot-booking-retrival"},
			RawBlock: "requirement:\n  route: AUH-LHR  # comment kept\n",
			Slots: []storage.SlotDetail{{Environment: "acp-dev", Class: "SHARED", LastOutcome: sp("valid"),
				Instances: []storage.InstanceRecord{
					{PNR: "ANJG2V", State: "VALID", TestData: []model.TestDatum{{PNR: "ANJG2V", LastName: "AdtA"}}, CreatedAt: now},
					{PNR: "US2UT2", State: "RETIRED", RetireReason: sp("dead"), CreatedAt: now, RetiredAt: &now},
				}}}}
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "testcases", "detail", page{Data: d})
		body := rec.Body.String()
		for _, want := range []string{"# comment kept", "ANJG2V", "AdtA", "US2UT2", "dead", "chatbot-booking-retrival"} {
			if !strings.Contains(body, want) {
				t.Errorf("detail fragment missing %q", want)
			}
		}
	})
	t.Run("log fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		logData := runLogData{Loc: time.UTC, RunID: run.ID, Live: true, Follow: true, Filter: "problems", Size: 50,
			Sizes: logPageSizes, Filters: logFilterOptions, Total: 120, Page: 3, Pages: 3, From: 101, To: 120,
			FirstURL: "/runs/x/log?page=1", PrevURL: "/runs/x/log?page=2", SelfURL: "/runs/x/log?page=last",
			Entries: []storage.LogEntry{{At: now, Level: storage.LogError, TestCaseKey: "ACP-TC-9", Message: "PSS timeout"}}}
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "run", "logfrag", page{Data: logData})
		body := rec.Body.String()
		for _, want := range []string{`id="run-log"`, `hx-trigger="every 3s"`, "Lines 101–120 of 120", "following live",
			"Page 3 of 3", "PSS timeout", "lvl-error"} {
			if !strings.Contains(body, want) {
				t.Errorf("log fragment missing %q", want)
			}
		}
		logData.Live = false
		rec = httptest.NewRecorder()
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "run", "logfrag", page{Data: logData})
		if strings.Contains(rec.Body.String(), "every 3s") {
			t.Error("a finished run's log must stop refreshing")
		}
	})
	t.Run("connection fragment", func(t *testing.T) {
		rec := httptest.NewRecorder()
		s.renderFragment(rec, httptest.NewRequest("GET", "/", nil), "settings", "connection",
			page{Data: connectionResult{Total: 6, WithBlock: []string{"ACP-TC-1", "ACP-TC-2"}}})
		if !strings.Contains(rec.Body.String(), "ACP-TC-1, ACP-TC-2") {
			t.Fatalf("got %s", rec.Body.String())
		}
	})
}
