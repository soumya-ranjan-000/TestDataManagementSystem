package model

import "time"

// InstanceState is a node in the instance lifecycle state machine from
// "The instance lifecycle".
type InstanceState string

const (
	StateGenerating InstanceState = "GENERATING"
	StateValid      InstanceState = "VALID"
	StateConsumed   InstanceState = "CONSUMED"
	StateExpired    InstanceState = "EXPIRED"
	StateDead       InstanceState = "DEAD"
	StateRetired    InstanceState = "RETIRED"
)

// RetireReason is why an instance left Valid. The three lifecycle failure
// reasons all lead to Retired, but the reason is kept for the debugging
// trail — "that record is the debugging trail, so it is never deleted."
type RetireReason string

const (
	ReasonConsumed RetireReason = "consumed"
	ReasonExpired  RetireReason = "expired"
	ReasonDead     RetireReason = "dead"
)

// Slot is permanent: it belongs to the test case forever and always holds
// one current instance plus a history of retired ones.
type Slot struct {
	ID              string
	TestCaseKey     string // e.g. "ACP-TC-1"
	QMetryTCID      string // QMetry's opaque test case id, e.g. "854KiLA8uKmAmv"
	Class           Class
	Environment     string
	Block           Block
	BlockHash       string // hash of the last-ingested normalized block; drives change detection
	OrphanedAt      *time.Time
	CurrentInstance *Instance
}

// Instance is disposable: the actual PNR filling a slot right now.
type Instance struct {
	ID           string
	SlotID       string
	PNR          string
	State        InstanceState
	RetireReason *RetireReason
	ReservedBy   *string // run/test identity holding this instance, for atomic reservation
	CreatedAt    time.Time
	RetiredAt    *time.Time
}
