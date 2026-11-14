// SPDX-License-Identifier: AGPL-3.0-or-later

package testdb

import "testing"

// BenchmarkOpenTestDB tracks the setup cost paid by each core fixture.
func BenchmarkOpenTestDB(b *testing.B) {
	for b.Loop() {
		db := Open(b)
		db.Close()
	}
}
