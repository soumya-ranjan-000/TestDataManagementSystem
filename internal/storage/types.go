package storage

import (
	"errors"
	"time"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

var (
	// ErrNotFound means the row doesn't exist or belongs to another team —
	// deliberately indistinguishable, so IDs can't be probed across teams.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyGenerating means the slot already has a live instance, so
	// another generation for it is in flight or has just finished.
	ErrAlreadyGenerating = errors.New("slot already has a live instance")
	// ErrRunInProgress means the team already has a running run.
	ErrRunInProgress = errors.New("team already has a running scan")
)

type Team struct {
	ID               string
	Name             string
	TCMProvider      string
	TCMProjectKey    string
	TCMCustomFieldID string
	TCMFolderPath    string
	RunTimes         []string
	Timezone         string
}

type Environment struct {
	ID         string
	Name       string
	PSSBaseURL string
}

type Recipient struct {
	ID           string
	Email        string
	OnScan       bool
	OnGeneration bool
	OnError      bool
}

type User struct {
	ID                 string
	TeamID             string
	Email              string
	Name               string
	PasswordHash       string
	Role               string
	MustChangePassword bool
	CreatedAt          time.Time
}

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

type RunMode string

const (
	ModeCheck      RunMode = "check"
	ModeHeal       RunMode = "heal"
	ModeRegenerate RunMode = "regenerate"
)

type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

// RunScope narrows a run. The zero value means "the team's whole folder,
// every environment" — the only scope that may orphan slots.
type RunScope struct {
	Environment string   `json:"environment,omitempty"`
	SlotIDs     []string `json:"slot_ids,omitempty"`
}

func (s RunScope) IsFull() bool { return s.Environment == "" && len(s.SlotIDs) == 0 }

type Run struct {
	ID           string
	TeamID       string
	Trigger      string
	Mode         RunMode
	Scope        RunScope
	Status       RunStatus
	RequestedBy  *string
	ScheduledFor *time.Time
	SyncError    *string
	Error        *string
	Counts       Counts
	CreatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

type Counts struct {
	Total     int
	Valid     int
	Invalid   int
	Generated int
	Errors    int
}

type Outcome string

const (
	OutcomeValid            Outcome = "valid"
	OutcomeInvalid          Outcome = "invalid"
	OutcomeRegenerated      Outcome = "regenerated"
	OutcomeGenerated        Outcome = "generated"
	OutcomeGenerationFailed Outcome = "generation_failed"
	OutcomeCheckError       Outcome = "check_error"
	OutcomeBlockError       Outcome = "block_error"
	OutcomeOrphaned         Outcome = "orphaned"
)

type RunItem struct {
	ID          string
	SlotID      *string
	TestCaseKey string
	Environment string
	Outcome     Outcome
	OldPNR      *string
	NewPNR      *string
	FailedRule  *string
	Error       *string
	CreatedAt   time.Time
}

// SlotUpsert is what sync writes for one test case in one environment.
type SlotUpsert struct {
	TestCaseKey   string
	TCMTestCaseID string
	TCMVersion    int
	Class         model.Class
	Environment   string
	Block         *model.Block
	BlockHash     string
}

// GeneratedInstance is one row of the test data creation report.
type GeneratedInstance struct {
	InstanceID   string
	TestCaseKey  string
	Environment  string
	PNR          string
	State        string
	RetireReason *string
	RunID        *string
	CreatedAt    time.Time
	RetiredAt    *time.Time
}
