// Package ingest is the sync/change-detection half of the self-healing
// loop: normalize the block so reformatting isn't mistaken for a real
// change, hash it, and flag only a real hash change onto the
// needs-updating list. See "Sync and change detection".
package ingest

import (
	"crypto/sha256"
	"encoding/hex"

	"gopkg.in/yaml.v3"

	"github.com/soumya-ranjan-000/tdms/internal/model"
)

// Hash produces a stable fingerprint of a block's meaning, not its
// formatting. Re-marshalling through the same struct fields (fixed key
// order, no comments, no incidental whitespace) means reformatting the
// source YAML never changes the hash — only an actual value change does.
func Hash(block *model.Block) (string, error) {
	canonical, err := yaml.Marshal(block)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
