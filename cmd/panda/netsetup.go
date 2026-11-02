// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"database/sql"
	"log/slog"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// ensureSharedSecret mints and persists network.shared_secret for a process
// that can own the node row. Both `panda daemon` and `panda web` do — the
// console sets MeshNode and is the mesh endpoint when no daemon holds the
// identity lock — so the bootstrap cannot live in one of them.
//
// A secret-less node cannot mesh at all: the WS listener refuses to start and
// discovery stays listen-only. Rather than booting a node that is silently
// unreachable, mint one and persist it so the next start (and any
// `panda nodes`/pair output) sees the same material. The secret creates a
// standalone mesh domain; a node joining an existing mesh later adopts the
// inviter's secret via pair.
//
// A node that HAS a mesh (configured peers, or registered remote nodes) but no
// secret almost certainly lost its credential. Minting a fresh one anyway
// would exile it permanently, so this screams and keeps the wrong value off
// disk; the caller still gets a usable in-memory secret to fail closed with.
func ensureSharedSecret(cfg *config.Config, db *sql.DB, selfID, configPath string, logger *slog.Logger) bool {
	if cfg.Network.SharedSecret != "" {
		return true
	}
	meshed := len(cfg.Network.Peers) > 0 || hasMeshHistory(db, selfID)
	secret, err := generateSharedSecret()
	if err != nil {
		logger.Warn("could not generate shared secret", "err", err)
		return false
	}
	cfg.Network.SharedSecret = secret
	if meshed {
		logger.Error("network.shared_secret is empty on a node that has meshed before — "+
			"a fresh in-memory secret was minted and configured peers WILL REJECT it. "+
			"If this node previously joined a mesh via OPENPANDA_SHARED_SECRET or `panda pair`, "+
			"stop it and restore the original secret; pairing again also re-adopts the mesh secret",
			"configured_peers", len(cfg.Network.Peers))
		return true
	}
	if werr := config.UpdateNetworkSection(configPath, config.NetworkConfig{SharedSecret: secret}); werr != nil {
		logger.Warn("generated shared secret could not be persisted (in-memory only)", "err", werr)
	} else {
		logger.Info("generated and saved shared secret", "config", configPath)
	}
	return true
}
