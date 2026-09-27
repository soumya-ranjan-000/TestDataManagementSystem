package model

import "time"

// InstanceState is a node in the instance lifecycle state machine from
// "The instance lifecycle". An instance moves straight from VALID to
// RETIRED (with a reason): the intermediate CONSUMED/EXPIRED/DEAD names are
// recorded as the retire reason instead, because any non-RETIRED state
// still occupies the slot's single live-instance position.
type InstanceState string

const (
	StateGenerating InstanceState = "GENERATING"
	StateValid      InstanceState = "VALID"
	StateRetired    InstanceState = "RETIRED"
)

// RetireReason is why an instance left the slot. The record is kept as the
// debugging trail — "that record is the debugging trail, so it is never
// deleted."
type RetireReason string

const (
	ReasonConsumed           RetireReason = "consumed"            // hit its terminal used-up state, e.g. CANCELLED
	ReasonExpired            RetireReason = "expired"             // a time rule lapsed, e.g. check-in window closed
	ReasonDead               RetireReason = "dead"                // the airline killed or purged it
	ReasonManual             RetireReason = "manual"              // a user asked for a fresh PNR
	ReasonRequirementChanged RetireReason = "requirement_changed" // the block was edited after generation
	ReasonGenerationFailed   RetireReason = "generation_failed"   // booked but a later generation step failed
)

// Slot is permanent: it belongs to the test case forever and always holds
// one current instance plus a history of retired ones. Its identity is
// (TeamID, TestCaseKey, Environment).
type Slot struct {
	ID            string
	TeamID        string
	TestCaseKey   string // e.g. "ACP-TC-1"
	TCMTestCaseID string // the test management app's opaque id, e.g. "854KiLA8uKmAmv"
	TCMVersion    int
	Class         Class
	Environment   string
	Block         Block
	BlockHash     string // hash of the last good normalized block; drives change detection
	BlockError    *string
	OrphanedAt    *time.Time
}

// Instance is disposable: the actual PNR filling a slot right now.
type Instance struct {
	ID           string
	SlotID       string
	PNR          string
	State        InstanceState
	BlockHash    string // the slot's block hash when this PNR was generated
	TestData     []TestDatum
	RetireReason *RetireReason
	CreatedAt    time.Time
	RetiredAt    *time.Time
}

// TestDatum is one piece of test data a test receives: a PNR and the last
// name needed to retrieve it. An instance holds a list of them (one today).
type TestDatum struct {
	PNR      string `json:"pnr"`
	LastName string `json:"last_name,omitempty"`
}
