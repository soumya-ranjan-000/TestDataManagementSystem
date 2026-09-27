// Package web is TDMS's browser UI: server-rendered html/template pages with
// htmx for the few live parts. Every page a signed-in user reaches gets a
// TeamRepo bound to their own team, so no handler can read or change
// another team's data.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/auth"
	"github.com/soumya-ranjan-000/tdms/internal/qmetry"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	store  *storage.Store
	qmetry *qmetry.Client
	// wake nudges the worker after a run is queued.
	wake         func()
	pages        map[string]*template.Template
	limiter      *loginLimiter
	secureCookie bool
}

type Config struct {
	Store  *storage.Store
	QMetry *qmetry.Client
	Wake   func()
	// InsecureCookies drops the Secure cookie flag, for plain-HTTP access on
	// a non-localhost address during development. Browsers already accept
	// Secure cookies on http://localhost.
	InsecureCookies bool
}

func New(cfg Config) (*Server, error) {
	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	wake := cfg.Wake
	if wake == nil {
		wake = func() {}
	}
	return &Server{
		store: cfg.Store, qmetry: cfg.QMetry, wake: wake, pages: pages,
		limiter: newLoginLimiter(10, 15*time.Minute), secureCookie: !cfg.InsecureCookies,
	}, nil
}

// Handler is the whole UI: routes, then CSRF protection, then security
// headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.Handle("POST /logout", s.signedIn(s.logout, true))
	mux.Handle("GET /account/password", s.signedIn(s.passwordPage, true))
	mux.Handle("POST /account/password", s.signedIn(s.changePassword, true))

	mux.Handle("GET /{$}", s.signedIn(s.dashboard, false))
	mux.Handle("GET /testcases", s.signedIn(s.testCases, false))
	mux.Handle("GET /testcases/{id}/detail", s.signedIn(s.testCaseDetail, false))
	mux.Handle("POST /testcases/action", s.signedIn(s.testCaseAction, false))
	mux.Handle("GET /scans/new", s.signedIn(s.newScan, false))
	mux.Handle("POST /scans", s.signedIn(s.startScan, false))
	mux.Handle("GET /runs", s.signedIn(s.runs, false))
	mux.Handle("GET /runs/{id}", s.signedIn(s.runPage, false))
	mux.Handle("GET /runs/{id}/live", s.signedIn(s.runLive, false))
	mux.Handle("GET /runs/{id}/log", s.signedIn(s.runLog, false))
	mux.Handle("GET /runs/{id}/log.txt", s.signedIn(s.runLogDownload, false))
	mux.Handle("GET /reports/generation", s.signedIn(s.generationReport, false))

	mux.Handle("GET /settings", s.admin(s.settings))
	mux.Handle("POST /settings/integration", s.admin(s.saveIntegration))
	mux.Handle("POST /settings/test-connection", s.admin(s.testConnection))
	mux.Handle("POST /settings/environments", s.admin(s.saveEnvironments))
	mux.Handle("POST /settings/schedule", s.admin(s.saveSchedule))
	mux.Handle("POST /settings/recipients", s.admin(s.addRecipient))
	mux.Handle("POST /settings/recipients/{id}", s.admin(s.updateRecipient))
	mux.Handle("POST /settings/recipients/{id}/delete", s.admin(s.deleteRecipient))
	mux.Handle("GET /settings/members", s.admin(s.members))
	mux.Handle("POST /settings/members", s.admin(s.addMember))
	mux.Handle("POST /settings/members/{id}/role", s.admin(s.setMemberRole))
	mux.Handle("POST /settings/members/{id}/reset", s.admin(s.resetMember))
	mux.Handle("POST /settings/members/{id}/delete", s.admin(s.deleteMember))

	// Go 1.25's cross-origin protection rejects state-changing requests a
	// browser sent from another site (Sec-Fetch-Site / Origin checks), which
	// is TDMS's CSRF defense.
	return securityHeaders(http.NewCrossOriginProtection().Handler(mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// --- request context --------------------------------------------------------

type ctxKey struct{}

type session struct {
	user      *storage.User
	repo      *storage.TeamRepo
	tokenHash []byte
}

func sessionFrom(r *http.Request) *session {
	sess, _ := r.Context().Value(ctxKey{}).(*session)
	return sess
}

// signedIn requires a valid session. Until a user replaces their temporary
// password, only handlers marked allowPending (the password page, logout)
// are reachable.
func (s *Server) signedIn(h http.HandlerFunc, allowPending bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil {
			s.redirectToLogin(w, r)
			return
		}
		hash := auth.HashToken(cookie.Value)
		user, err := s.store.SessionUser(r.Context(), hash, auth.IdleTimeout)
		if errors.Is(err, storage.ErrNotFound) {
			s.clearCookie(w)
			s.redirectToLogin(w, r)
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if user.MustChangePassword && !allowPending {
			redirect(w, r, "/account/password")
			return
		}
		sess := &session{user: user, repo: s.store.Team(user.TeamID), tokenHash: hash}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return s.signedIn(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r).user.Role != storage.RoleAdmin {
			http.Error(w, "Only team admins can open this page.", http.StatusForbidden)
			return
		}
		h(w, r)
	}, false)
}

func (s *Server) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, "/login")
}

// redirect works for both full page loads and htmx requests (which need an
// HX-Redirect header rather than a 3xx they'd follow into a fragment swap).
func redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/",
		MaxAge: int(auth.SessionLifetime.Seconds()), HttpOnly: true, Secure: s.secureCookie,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

// --- rendering --------------------------------------------------------------

type page struct {
	Title  string
	Active string
	User   *storage.User
	Team   *storage.Team
	Loc    *time.Location
	Notice string
	Error  string
	Data   any
}

func parsePages() (map[string]*template.Template, error) {
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	pages := map[string]*template.Template{}
	for _, name := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(name, "templates/"), ".html")
		if base == "layout" {
			continue
		}
		t, err := template.New(base).Funcs(funcs).ParseFS(templateFS, "templates/layout.html", name)
		if err != nil {
			return nil, err
		}
		pages[base] = t
	}
	return pages, nil
}

// render writes a full page: the layout wrapping the named page template.
func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, p page) {
	s.execute(w, r, status, name, "layout", p)
}

// renderFragment writes one named template block, for htmx swaps.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, name, block string, p page) {
	s.execute(w, r, http.StatusOK, name, block, p)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request, status int, name, block string, p page) {
	t, ok := s.pages[name]
	if !ok {
		s.serverError(w, r, errors.New("unknown page "+name))
		return
	}
	if sess := sessionFrom(r); sess != nil {
		p.User = sess.user
		if p.Team == nil {
			if team, err := sess.repo.Settings(r.Context()); err == nil {
				p.Team = team
			}
		}
	}
	p.Loc = time.UTC
	if p.Team != nil {
		if loc, err := time.LoadLocation(p.Team.Timezone); err == nil {
			p.Loc = loc
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, block, p); err != nil {
		log.Printf("web: rendering %s/%s: %v", name, block, err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("web: %s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// pathID returns the {id} path value if it is a well-formed UUID; anything
// else is treated as not found without touching the database.
func pathID(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	return id, uuidPattern.MatchString(id)
}

var funcs = template.FuncMap{
	"tm": func(loc *time.Location, v any) string {
		var t time.Time
		switch x := v.(type) {
		case time.Time:
			t = x
		case *time.Time:
			if x == nil {
				return "—"
			}
			t = *x
		default:
			return "—"
		}
		if t.IsZero() {
			return "—"
		}
		return t.In(loc).Format("2006-01-02 15:04 MST")
	},
	"clock": func(loc *time.Location, t time.Time) string {
		return t.In(loc).Format("15:04:05.000")
	},
	"deref": func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	},
	"shortID": func(id string) string {
		if len(id) > 8 {
			return id[:8]
		}
		return id
	},
	"lower": strings.ToLower,
}
