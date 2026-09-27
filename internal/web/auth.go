package web

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/auth"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

// loginLimiter caps failed logins per client IP and per email within a
// window, slowing password guessing against any one account or from any one
// machine. In-memory is enough for a single TDMS process.
type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, failures: map[string][]time.Time{}}
}

func (l *loginLimiter) recent(key string, now time.Time) []time.Time {
	var kept []time.Time
	for _, t := range l.failures[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if kept == nil {
		delete(l.failures, key)
	} else {
		l.failures[key] = kept
	}
	return kept
}

func (l *loginLimiter) blocked(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		if len(l.recent(k, now)) >= l.max {
			return true
		}
	}
	return false
}

func (l *loginLimiter) fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for _, k := range keys {
		l.failures[k] = append(l.recent(k, now), now)
	}
}

func (l *loginLimiter) reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.failures, k)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "login", page{Title: "Sign in", Data: ""})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	keys := []string{"ip:" + clientIP(r), "email:" + email}

	fail := func(msg string, status int) {
		s.render(w, r, status, "login", page{Title: "Sign in", Error: msg, Data: email})
	}
	if s.limiter.blocked(keys...) {
		fail("Too many failed sign-in attempts. Try again in a few minutes.", http.StatusTooManyRequests)
		return
	}

	user, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}
	hash := ""
	if user != nil {
		hash = user.PasswordHash
	}
	// An unknown email still costs a full bcrypt compare, so response time
	// doesn't reveal which emails have accounts.
	if !auth.CheckPassword(hash, password) {
		s.limiter.fail(keys...)
		fail("Wrong email or password.", http.StatusUnauthorized)
		return
	}
	s.limiter.reset(keys...)

	// A brand-new session on every login, so a token planted before sign-in
	// can never become an authenticated one.
	token, tokenHash, err := auth.NewSessionToken()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.CreateSession(r.Context(), tokenHash, user.ID, time.Now().Add(auth.SessionLifetime)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.setSessionCookie(w, token)
	if user.MustChangePassword {
		redirect(w, r, "/account/password")
		return
	}
	redirect(w, r, "/")
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSession(r.Context(), sessionFrom(r).tokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.clearCookie(w)
	redirect(w, r, "/login")
}

func (s *Server) passwordPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "password", page{Title: "Change password"})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r)
	current, next, confirm := r.FormValue("current"), r.FormValue("new"), r.FormValue("confirm")
	fail := func(msg string) {
		s.render(w, r, http.StatusBadRequest, "password", page{Title: "Change password", Error: msg})
	}
	if !auth.CheckPassword(sess.user.PasswordHash, current) {
		fail("Your current password is wrong.")
		return
	}
	if next != confirm {
		fail("The new passwords don't match.")
		return
	}
	if next == current {
		fail("Choose a password different from the current one.")
		return
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		fail(err.Error())
		return
	}
	// Other sessions are signed out; this one stays.
	if err := s.store.SetOwnPassword(r.Context(), sess.user.ID, hash, sess.tokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/?notice=password")
}
