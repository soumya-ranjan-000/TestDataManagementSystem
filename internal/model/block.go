// Package model holds the plain data shapes TDMS passes between packages:
// the authoring block QA writes (Requirement/Validity), and the running
// state TDMS owns (Slot/Instance). See the design doc's "Requirement vs
// rules" and "Slots and instances" sections.
package model

import "gopkg.in/yaml.v3"

// Class is whether a test mutates its PNR (OWNED) or only reads it (SHARED).
type Class string

const (
	ClassOwned  Class = "OWNED"
	ClassShared Class = "SHARED"
)

// Requirement is the Do's: the shape the PNR must have. Fixed at creation,
// mostly stable — see "Requirement vs rules: Do's and Don'ts".
type Requirement struct {
	Airline     string            `yaml:"airline"`
	TripType    string            `yaml:"trip_type"`
	Passengers  map[string]int    `yaml:"passengers"`
	Route       string            `yaml:"route"`
	Depart      string            `yaml:"depart"` // relative date expression, e.g. "T+90d"
	Cabin       string            `yaml:"cabin"`
	Ticketed    bool              `yaml:"ticketed"`
	Ancillaries map[string]string `yaml:"ancillaries,omitempty"`
}

// Rule is one Don't. It decodes generically — Name plus a raw Params map —
// so an unknown or malformed rule template fails as a clear dictionary
// validation error later, not as an opaque YAML decode error here. See
// dictionary.Compile for how Name/Params become a legal, generic check.
type Rule struct {
	Name   string
	Params map[string]any
}

// UnmarshalYAML decodes a rule written as a single-key mapping, e.g.
// `checkin_window: { opens_hrs: 48, closes_hrs: 2 }`.
func (r *Rule) UnmarshalYAML(value *yaml.Node) error {
	var raw map[string]yaml.Node
	if err := value.Decode(&raw); err != nil {
		return err
	}
	for name, paramsNode := range raw {
		var params map[string]any
		if err := paramsNode.Decode(&params); err != nil {
			return err
		}
		r.Name = name
		r.Params = params
		break // exactly one key is expected; dictionary.Compile rejects the rest
	}
	return nil
}

// Validity is the Don'ts: conditions that must not become true. Erode
// through time, use, or the airline — this is what drives the live health
// check.
type Validity struct {
	Class Class  `yaml:"class"`
	Rules []Rule `yaml:"rules"`
}

// Block is the whole authoring block QA writes into the QMetry custom
// field: one YAML document holding both the requirement and the validity
// rules, per "The authoring block".
type Block struct {
	Requirement Requirement `yaml:"requirement"`
	Validity    Validity    `yaml:"validity"`
}
