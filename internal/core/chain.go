package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// hashEvent computes the chained hash of one task event. The chain links rows
// within a single task: each event's prev_hash is the hash of the previous
// event for that task, and the genesis event uses an empty prev_hash.
func hashEvent(prevHash, taskID string, ts int64, typ, dataJSON string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%s|%s|%s", prevHash, ts, taskID, typ, dataJSON)
	return hex.EncodeToString(h.Sum(nil))
}
