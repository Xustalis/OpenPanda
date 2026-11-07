// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// ErrConflict means a memory file changed since the editor loaded it.
var ErrConflict = errors.New("memory: file changed; reload before saving")

// Version is a stable digest of a memory snapshot, including the empty file.
func Version(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
