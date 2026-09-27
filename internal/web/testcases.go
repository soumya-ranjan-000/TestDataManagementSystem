package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

var pageSizes = []int{5, 10, 20, 50, 100}

const (
	defaultPageSize = 10
	pageSizeCookie  = "tdms_page_size"
)

// tcState is the table's state, all of it carried in the URL so paging,
// the back button, refresh and bookmarks keep the user's place.
type tcState struct {
	Folder string // folder id, "" = all
	Q      string
	Filter string
	Env    string
	Page   int
	Size   int
}

func (st tcState) url(override func(*tcState)) string {
	s := st
	if override != nil {
		override(&s)
	}
	v := url.Values{}
	if s.Folder != "" {
		v.Set("folder", s.Folder)
	}
	if s.Q != "" {
		v.Set("q", s.Q)
	}
	if s.Filter != "" && s.Filter != string(storage.FilterAll) {
		v.Set("filter", s.Filter)
	}
	if s.Env != "" {
		v.Set("env", s.Env)
	}
	if s.Page > 1 {
		v.Set("page", strconv.Itoa(s.Page))
	}
	v.Set("size", strconv.Itoa(s.Size))
	return "/testcases?" + v.Encode()
}

type folderLink struct {
	storage.Folder
	URL    string
	Active bool
}

type option struct{ Value, Label string }

type testCasesData struct {
	State        tcState
	Rows         []storage.TestCaseRow
	Folders      []folderLink
	AllURL       string
	AllCount     int
	Environments []storage.Environment
	Filters      []option
	Sizes        []int

	Total, Page, Pages, From, To        int
	FirstURL, PrevURL, NextURL, LastURL string
}

var filterOptions = []option{
	{string(storage.FilterAll), "All test cases"},
	{string(storage.FilterWithBlock), "With TDMS block"},
	{string(storage.FilterNoBlock), "Without TDMS block"},
	{string(storage.FilterAttention), "Needs attention"},
}

// pageSize takes ?size= when it's one of the offered sizes (remembering it
// in a cookie), else the remembered size, else the default.
func (s *Server) pageSize(w http.ResponseWriter, r *http.Request) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && slices.Contains(pageSizes, n) {
		http.SetCookie(w, &http.Cookie{Name: pageSizeCookie, Value: strconv.Itoa(n), Path: "/",
			MaxAge: 365 * 24 * 3600, HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteLaxMode})
		return n
	}
	if c, err := r.Cookie(pageSizeCookie); err == nil {
		if n, err := strconv.Atoi(c.Value); err == nil && slices.Contains(pageSizes, n) {
			return n
		}
	}
	return defaultPageSize
}

func (s *Server) testCases(w http.ResponseWriter, r *http.Request) {
	repo := sessionFrom(r).repo
	ctx := r.Context()
	query := r.URL.Query()

	st := tcState{
		Folder: query.Get("folder"),
		Q:      strings.TrimSpace(query.Get("q")),
		Filter: query.Get("filter"),
		Env:    query.Get("env"),
		Size:   s.pageSize(w, r),
	}
	if len(st.Q) > 200 {
		st.Q = st.Q[:200]
	}
	if !storage.ValidFilter(st.Filter) {
		st.Filter = string(storage.FilterAll)
	}
	st.Page, _ = strconv.Atoi(query.Get("page"))
	st.Page = max(st.Page, 1)

	envs, err := repo.Environments(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !slices.ContainsFunc(envs, func(e storage.Environment) bool { return e.Name == st.Env }) {
		st.Env = ""
		if len(envs) > 0 {
			st.Env = envs[0].Name
		}
	}

	folders, err := repo.Folders(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var folderIDs []int64
	if id, err := strconv.ParseInt(st.Folder, 10, 64); err == nil &&
		slices.ContainsFunc(folders, func(f storage.Folder) bool { return f.ID == id }) {
		folderIDs = storage.DescendantFolderIDs(folders, id)
	} else {
		st.Folder = ""
	}

	q := storage.TestCaseQuery{FolderIDs: folderIDs, Search: st.Q, Filter: storage.TestCaseFilter(st.Filter),
		Environment: st.Env, Limit: st.Size, Offset: (st.Page - 1) * st.Size}
	rows, total, err := repo.TestCasePage(ctx, q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pages := max((total+st.Size-1)/st.Size, 1)
	if st.Page > pages {
		// The filter shrank under a stale page number: show the last page.
		st.Page = pages
		q.Offset = (st.Page - 1) * st.Size
		if rows, total, err = repo.TestCasePage(ctx, q); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	allCount, err := repo.CountTestCases(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	d := testCasesData{
		State: st, Rows: rows, Environments: envs, Filters: filterOptions, Sizes: pageSizes,
		Total: total, Page: st.Page, Pages: pages, AllCount: allCount,
		AllURL: st.url(func(s *tcState) { s.Folder, s.Page = "", 1 }),
	}
	if total > 0 {
		d.From = (st.Page-1)*st.Size + 1
		d.To = d.From + len(rows) - 1
	}
	if st.Page > 1 {
		d.FirstURL = st.url(func(s *tcState) { s.Page = 1 })
		d.PrevURL = st.url(func(s *tcState) { s.Page-- })
	}
	if st.Page < pages {
		d.NextURL = st.url(func(s *tcState) { s.Page++ })
		d.LastURL = st.url(func(s *tcState) { s.Page = pages })
	}
	for _, f := range folders {
		id := strconv.FormatInt(f.ID, 10)
		d.Folders = append(d.Folders, folderLink{Folder: f, Active: id == st.Folder,
			URL: st.url(func(s *tcState) { s.Folder, s.Page = id, 1 })})
	}

	// htmx asks for just the part it will swap: the results (paging, search,
	// filters) or the whole browser (a folder click, which also moves the
	// tree's highlight). A history restore needs the full page.
	w.Header().Set("Vary", "HX-Request, HX-Target")
	p := page{Title: "Test cases", Active: "testcases", Data: d}
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true" {
		switch r.Header.Get("HX-Target") {
		case "tc-results":
			s.renderFragment(w, r, "testcases", "results", p)
			return
		case "tc-browser":
			s.renderFragment(w, r, "testcases", "browser", p)
			return
		}
	}
	s.render(w, r, http.StatusOK, "testcases", p)
}

// testCaseDetail is the lazily loaded expansion of one row: the raw YAML
// block and each environment's PNR history.
func (s *Server) testCaseDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	d, err := sessionFrom(r).repo.TestCaseDetail(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "testcases", "detail", page{Data: d})
}
