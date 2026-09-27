package qmetry

import (
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// codeMacro matches Jira wiki-markup's {code[:language]}...{code} wrapper,
// which QMetry's own multi-line-textbox editor adds automatically when a
// human hand-edits the field through the UI (confirmed live: saving via
// the browser wrapped pasted YAML in {code:java}...{code}). A block TDMS
// writes itself never needs the wrapper, but ingest must tolerate it
// either way — "TDMS still validates every block when it reads it",
// because a field can always be hand-edited afterwards.
var codeMacro = regexp.MustCompile(`(?s)^\s*\{code(:[^}]*)?\}\s*(.*?)\s*\{code\}\s*$`)

// UnwrapCodeMacro strips an optional {code:...}...{code} wrapper, returning
// the input unchanged if the wrapper isn't present.
func UnwrapCodeMacro(raw string) string {
	if m := codeMacro.FindStringSubmatch(raw); m != nil {
		return m[2]
	}
	return raw
}

// ParseBlock unwraps and YAML-parses a test case's TDMS custom field value
// into the requirement+validity block.
func ParseBlock(raw string) (*model.Block, error) {
	yamlText := strings.TrimSpace(UnwrapCodeMacro(raw))
	var block model.Block
	if err := yaml.Unmarshal([]byte(yamlText+"\n"), &block); err != nil {
		return nil, err
	}
	return &block, nil
}
