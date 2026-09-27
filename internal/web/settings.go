package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/auth"
	"github.com/soumya-ranjan-000/tdms/internal/scan"
	"github.com/soumya-ranjan-000/tdms/internal/scheduler"
	"github.com/soumya-ranjan-000/tdms/internal/storage"
)

type settingsData struct {
	Available  []storage.Environment
	Enrolled   map[string]bool
	RunTimes   string
	Recipients []storage.Recipient
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request, status int, p page) {
	repo := sessionFrom(r).repo
	ctx := r.Context()
	team, err := repo.Settings(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	available, err := repo.AvailableEnvironments(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	enrolled, err := repo.Environments(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	recipients, err := repo.Recipients(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := settingsData{Available: available, Enrolled: map[string]bool{},
		RunTimes: strings.Join(team.RunTimes, ", "), Recipients: recipients}
	for _, e := range enrolled {
		d.Enrolled[e.ID] = true
	}
	p.Title, p.Active, p.Team, p.Data = "Settings", "settings", team, d
	s.render(w, r, status, "settings", p)
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	s.settingsPage(w, r, http.StatusOK, page{Notice: notice(r)})
}

func (s *Server) settingsError(w http.ResponseWriter, r *http.Request, msg string) {
	s.settingsPage(w, r, http.StatusBadRequest, page{Error: msg})
}

var (
	fieldIDPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	folderPattern  = regexp.MustCompile(`^[^\x00-\x1f]{0,500}$`)
)

func (s *Server) saveIntegration(w http.ResponseWriter, r *http.Request) {
	field := strings.TrimSpace(r.FormValue("custom_field_id"))
	folder := strings.TrimSpace(r.FormValue("folder_path"))
	if !fieldIDPattern.MatchString(field) {
		s.settingsError(w, r, "The custom field id must be letters, digits or underscores (e.g. qcf_7932330).")
		return
	}
	if !folderPattern.MatchString(folder) {
		s.settingsError(w, r, "The folder path is too long or contains control characters.")
		return
	}
	if err := sessionFrom(r).repo.UpdateIntegration(r.Context(), field, folder); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=integration")
}

type connectionResult struct {
	Err       string
	Total     int
	WithBlock []string
}

// testConnection lists the test cases the unsaved form values would scan,
// without saving them — so an admin can check a folder path before relying
// on it.
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	res := connectionResult{}
	defer func() { s.renderFragment(w, r, "settings", "connection", page{Data: res}) }()

	if s.qmetry == nil {
		res.Err = "QMetry credentials are not configured on the server."
		return
	}
	team, err := sessionFrom(r).repo.Settings(r.Context())
	if err != nil {
		res.Err = "Could not load team settings."
		return
	}
	team.TCMCustomFieldID = strings.TrimSpace(r.FormValue("custom_field_id"))
	team.TCMFolderPath = strings.TrimSpace(r.FormValue("folder_path"))
	if !fieldIDPattern.MatchString(team.TCMCustomFieldID) {
		res.Err = "Enter a valid custom field id first."
		return
	}
	src, err := scan.NewSource(team, s.qmetry)
	if err != nil {
		res.Err = err.Error()
		return
	}
	listing, err := src.List(r.Context())
	if err != nil {
		res.Err = err.Error()
		return
	}
	res.Total = len(listing.Cases)
	for _, c := range listing.Cases {
		if strings.TrimSpace(c.RawBlock) != "" {
			res.WithBlock = append(res.WithBlock, c.Key)
		}
	}
}

func (s *Server) saveEnvironments(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var ids []string
	for _, id := range r.Form["environment_id"] {
		if uuidPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		s.settingsError(w, r, "Select at least one environment.")
		return
	}
	if err := sessionFrom(r).repo.SetEnvironments(r.Context(), ids); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=environments")
}

func (s *Server) saveSchedule(w http.ResponseWriter, r *http.Request) {
	runTimes, err := scheduler.ParseRunTimes(r.FormValue("run_times"))
	if err != nil {
		s.settingsError(w, r, "Run times: "+err.Error())
		return
	}
	if len(runTimes) > 24 {
		s.settingsError(w, r, "At most 24 run times a day.")
		return
	}
	tz := strings.TrimSpace(r.FormValue("timezone"))
	if _, err := time.LoadLocation(tz); err != nil || tz == "" {
		s.settingsError(w, r, fmt.Sprintf("%q is not a known timezone (use a name like Asia/Dubai or UTC).", tz))
		return
	}
	if err := sessionFrom(r).repo.UpdateSchedule(r.Context(), runTimes, tz); err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=schedule")
}

// parseEmail accepts one bare address only — no display names or lists —
// so nothing extra can ride along into mail headers.
func parseEmail(v string) (string, bool) {
	v = strings.TrimSpace(v)
	addr, err := mail.ParseAddress(v)
	if err != nil || addr.Address != v || addr.Name != "" {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

func recipientFlags(r *http.Request) (scan, gen, errs bool) {
	return r.FormValue("on_scan") == "on", r.FormValue("on_generation") == "on", r.FormValue("on_error") == "on"
}

func (s *Server) addRecipient(w http.ResponseWriter, r *http.Request) {
	email, ok := parseEmail(r.FormValue("email"))
	if !ok {
		s.settingsError(w, r, "Enter a single valid email address.")
		return
	}
	onScan, onGen, onErr := recipientFlags(r)
	if !onScan && !onGen && !onErr {
		s.settingsError(w, r, "Pick at least one report for this recipient.")
		return
	}
	err := sessionFrom(r).repo.AddRecipient(r.Context(),
		storage.Recipient{Email: email, OnScan: onScan, OnGeneration: onGen, OnError: onErr})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=recipients")
}

func (s *Server) updateRecipient(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	onScan, onGen, onErr := recipientFlags(r)
	err := sessionFrom(r).repo.UpdateRecipient(r.Context(),
		storage.Recipient{ID: id, OnScan: onScan, OnGeneration: onGen, OnError: onErr})
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=recipients")
}

func (s *Server) deleteRecipient(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := sessionFrom(r).repo.DeleteRecipient(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	redirect(w, r, "/settings?notice=recipients")
}

// --- members ----------------------------------------------------------------

type membersData struct {
	Members []storage.User
	// TempPassword is shown exactly once, right after it is generated.
	TempPasswordFor string
	TempPassword    string
}

func (s *Server) membersPage(w http.ResponseWriter, r *http.Request, status int, p page, d membersData) {
	members, err := sessionFrom(r).repo.Members(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Members = members
	p.Title, p.Active, p.Data = "Members", "members", d
	s.render(w, r, status, "members", p)
}

func (s *Server) members(w http.ResponseWriter, r *http.Request) {
	s.membersPage(w, r, http.StatusOK, page{}, membersData{})
}

func validRole(role string) bool { return role == storage.RoleAdmin || role == storage.RoleMember }

func (s *Server) addMember(w http.ResponseWriter, r *http.Request) {
	email, ok := parseEmail(r.FormValue("email"))
	role := r.FormValue("role")
	name := strings.TrimSpace(r.FormValue("name"))
	if !ok || !validRole(role) || len(name) > 100 {
		s.membersPage(w, r, http.StatusBadRequest, page{Error: "Enter a valid email, a name under 100 characters and a role."}, membersData{})
		return
	}
	password, hash, err := newTempPassword()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := sessionFrom(r).repo.CreateMember(r.Context(), email, name, role, hash); err != nil {
		s.membersPage(w, r, http.StatusBadRequest, page{Error: err.Error()}, membersData{})
		return
	}
	s.membersPage(w, r, http.StatusOK, page{Notice: "Member added. Share the temporary password below with them; it is shown only once."},
		membersData{TempPasswordFor: email, TempPassword: password})
}

func (s *Server) setMemberRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	role := r.FormValue("role")
	if !ok || !validRole(role) {
		http.NotFound(w, r)
		return
	}
	s.memberChange(w, r, sessionFrom(r).repo.SetMemberRole(r.Context(), id, role), "Role updated.")
}

func (s *Server) deleteMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if id == sessionFrom(r).user.ID {
		s.membersPage(w, r, http.StatusBadRequest, page{Error: "You can't remove yourself."}, membersData{})
		return
	}
	s.memberChange(w, r, sessionFrom(r).repo.DeleteMember(r.Context(), id), "Member removed.")
}

func (s *Server) resetMember(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	password, hash, err := newTempPassword()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	repo := sessionFrom(r).repo
	err = repo.ResetMemberPassword(r.Context(), id, hash)
	if errors.Is(err, storage.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	email := ""
	if members, err := repo.Members(r.Context()); err == nil {
		for _, m := range members {
			if m.ID == id {
				email = m.Email
			}
		}
	}
	s.membersPage(w, r, http.StatusOK, page{Notice: "Password reset and the member signed out everywhere. The new temporary password is shown only once."},
		membersData{TempPasswordFor: email, TempPassword: password})
}

func (s *Server) memberChange(w http.ResponseWriter, r *http.Request, err error, okMsg string) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, storage.ErrLastAdmin):
		s.membersPage(w, r, http.StatusBadRequest, page{Error: "A team must keep at least one admin."}, membersData{})
	case err != nil:
		s.serverError(w, r, err)
	default:
		s.membersPage(w, r, http.StatusOK, page{Notice: okMsg}, membersData{})
	}
}

func newTempPassword() (password, hash string, err error) {
	if password, err = auth.TempPassword(); err != nil {
		return "", "", err
	}
	hash, err = auth.HashPassword(password)
	return password, hash, err
}
