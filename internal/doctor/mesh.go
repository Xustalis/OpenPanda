// SPDX-License-Identifier: AGPL-3.0-or-later

package doctor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// checkMesh answers the cross-network question a doctor run can answer from
// the store alone (design §8): does this node have anywhere to rendezvous,
// how much of its directory arrived by gossip, and is anything currently
// suspended as unpunchable? The live half — the elected edge count, dir
// gossip lag against each neighbour — needs the daemon's in-memory state and
// is reported by `panda status` instead, so it is deliberately absent here
// rather than faked from a table.
func checkMesh(cfg *config.Config) Check {
	if cfg.Network.BootstrapEdges == 0 {
		// Manual mode: the operator asked for no rendezvous edges, so having
		// none is the intended state, not a fault.
		return pass("doctor.mesh.manual", "k", "0")
	}
	db, err := sql.Open("sqlite",
		fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(2000)", escapeURIPath(cfg.Storage.DBPath)))
	if err != nil {
		return Check{}
	}
	defer db.Close()

	rows, err := db.Query(`SELECT COALESCE(relay,''), COALESCE(addrs,''), COALESCE(status,''), COALESCE(last_seen,0)
		FROM employee_cache`)
	if err != nil {
		return Check{}
	}
	defer rows.Close()
	var rendezvous, withAddrs, total int
	now := time.Now().Unix()
	for rows.Next() {
		var relay, addrs, status string
		var lastSeen int64
		if err := rows.Scan(&relay, &addrs, &status, &lastSeen); err != nil {
			return Check{}
		}
		total++
		var rp struct {
			Rendezvous bool `json:"rendezvous"`
		}
		online := status == "online" && lastSeen > 0 && now-lastSeen <= 90
		if online && relay != "" && json.Unmarshal([]byte(relay), &rp) == nil && rp.Rendezvous {
			rendezvous++
		}
		if addrs != "" && addrs != "null" && addrs != "[]" {
			withAddrs++
		}
	}
	if err := rows.Err(); err != nil {
		return Check{}
	}

	var raw string
	suspect := 0
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'nat_state'`).Scan(&raw); err == nil {
		var st struct {
			Until map[string]int64 `json:"until"`
		}
		if json.Unmarshal([]byte(raw), &st) == nil {
			for _, until := range st.Until {
				if until > now {
					suspect++
				}
			}
		}
	}

	k := fmt.Sprintf("%d", cfg.Network.BootstrapEdges)
	if rendezvous == 0 {
		// A single node with no peers configured has nowhere to rendezvous
		// by definition; that is the normal loopback install, not a failed
		// self-check. Only a node that declared peers but reached none is a
		// problem worth an exit code.
		if len(cfg.Network.Peers) == 0 {
			return pass("doctor.mesh.single", "k", k)
		}
		// Honest degradation, reported not hidden: direct and punch paths keep
		// working, and the mesh is whatever the live connections already are.
		return fail("doctor.mesh.none",
			"k", k, "rows", fmt.Sprintf("%d", total), "suspect", fmt.Sprintf("%d", suspect))
	}
	return pass("doctor.mesh.ok",
		"k", k, "rendezvous", fmt.Sprintf("%d", rendezvous),
		"rows", fmt.Sprintf("%d", total), "addrs", fmt.Sprintf("%d", withAddrs),
		"suspect", fmt.Sprintf("%d", suspect))
}
