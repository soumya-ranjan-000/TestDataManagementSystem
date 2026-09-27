package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestStateURLKeepsEverythingAndResetsOnlyWhatsOverridden(t *testing.T) {
	st := tcState{Folder: "7", Q: "retr ieve", Filter: "attention", Env: "acp-dev", Page: 3, Size: 20}
	u, _ := url.Parse(st.url(func(s *tcState) { s.Page++ }))
	q := u.Query()
	want := map[string]string{"folder": "7", "q": "retr ieve", "filter": "attention", "env": "acp-dev", "page": "4", "size": "20"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q (url %s)", k, q.Get(k), v, u)
		}
	}
	if st.Page != 3 {
		t.Fatal("building a URL must not mutate the state")
	}
	// Page 1 and the default filter are left out to keep URLs short.
	first := tcState{Filter: "all", Page: 1, Size: 10}.url(nil)
	if first != "/testcases?size=10" {
		t.Fatalf("got %s", first)
	}
}

func TestPageSizeRemembersValidChoiceOnly(t *testing.T) {
	s := &Server{}
	cases := []struct {
		query, cookie string
		want          int
		setsCookie    bool
	}{
		{"", "", defaultPageSize, false},
		{"size=50", "", 50, true},
		{"", "20", 20, false},
		{"size=7", "20", 20, false},  // not an offered size: ignored
		{"size=100", "5", 100, true}, // an explicit choice beats the remembered one
		{"", "9999", defaultPageSize, false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/testcases?"+tc.query, nil)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: pageSizeCookie, Value: tc.cookie})
		}
		rec := httptest.NewRecorder()
		if got := s.pageSize(rec, req); got != tc.want {
			t.Errorf("query %q cookie %q: got %d, want %d", tc.query, tc.cookie, got, tc.want)
		}
		if set := rec.Header().Get("Set-Cookie") != ""; set != tc.setsCookie {
			t.Errorf("query %q: set cookie = %v, want %v", tc.query, set, tc.setsCookie)
		}
	}
}

func TestRunLogURLKeepsFiltersAndPage(t *testing.T) {
	d := runLogData{RunID: "r1", Filter: "errors", Q: "TC-9", Size: 200}
	u, _ := url.Parse(d.url("last"))
	q := u.Query()
	if u.Path != "/runs/r1/log" || q.Get("level") != "errors" || q.Get("q") != "TC-9" || q.Get("size") != "200" || q.Get("page") != "last" {
		t.Fatalf("got %s", u)
	}
	if all := (runLogData{RunID: "r1", Filter: "all", Size: 100}).url("1"); strings.Contains(all, "level=") {
		t.Fatalf("the default level should be left out: %s", all)
	}
}
