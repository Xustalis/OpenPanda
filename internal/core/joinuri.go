// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/config"
)

// Join URI — the cold-start kernel of the cross-network design (docs/protocol.md). Zero
// information means zero discovery, and that is physics rather than a defect:
// a node that has never heard of the mesh needs one address to knock on.
//
//	panda://join?b=203.0.113.5:7836&b=node.example.com:7836&c=482913&k=k:<pub>
//
//	b  repeatable contact addresses. ANY online member can serve as one —
//	   there is no privileged bootstrap server, and the list is tried in
//	   order until one answers.
//	k  the introducer's stable Ed25519 identity (`k:<hex>` or bare hex).
//	   Optional but strongly recommended: the joiner refuses to accept a
//	   secret from a responder whose pair_ready pub does not match, which
//	   turns a poisoned bootstrap list into "wrong number" instead of
//	   "joined the attacker's mesh".
//	c  the expected pairing code (the SAS the introducer's console shows,
//	   with or without its dash). When present the joiner checks it
//	   automatically — a mismatch IS the MITM detection, so there is no
//	   human comparison left to skip.
//
// The scheme accepts both "panda" and "openpanda" (the design's open
// question docs/protocol.md); parsing is deliberately tolerant so a URI outlives a
// rename.
type JoinSpec struct {
	// Bootstraps are contact addresses, dialable in their bare host:port or
	// ws(s):// form.
	Bootstraps []string
	// Introducer is the expected responder Ed25519 pubkey in hex, "" when
	// the URI pinned none.
	Introducer string
	// Code is the expected SAS digits (dash optional), "" when absent.
	Code string
}

// ParseJoinURI parses the join form. It validates addresses the same way the
// peer list does, so a URI cannot introduce an address `nodes add` would
// refuse.
func ParseJoinURI(raw string) (*JoinSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty join URI")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse join URI: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "panda", "openpanda":
	default:
		return nil, fmt.Errorf("join URI scheme %q is not panda/openpanda", u.Scheme)
	}
	verb := strings.ToLower(u.Host)
	if verb == "" {
		verb = strings.Trim(strings.ToLower(u.Path), "/")
	}
	if verb != "join" {
		return nil, fmt.Errorf("join URI names %q, want join", verb)
	}
	q := u.Query()
	spec := &JoinSpec{}
	for _, b := range q["b"] {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		if err := config.ValidatePeerAddr(b); err != nil {
			return nil, fmt.Errorf("join URI bootstrap %q: %w", b, err)
		}
		spec.Bootstraps = append(spec.Bootstraps, b)
	}
	if len(spec.Bootstraps) == 0 {
		return nil, fmt.Errorf("join URI carries no bootstrap address (b=)")
	}
	if k := strings.TrimSpace(q.Get("k")); k != "" {
		k = strings.TrimPrefix(k, stableIDPrefix) // "k:<hex>"
		raw, err := hex.DecodeString(k)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("join URI introducer key %q is not a 32-byte hex Ed25519 key", q.Get("k"))
		}
		spec.Introducer = strings.ToLower(k)
	}
	if c := strings.TrimSpace(q.Get("c")); c != "" {
		digits := strings.ReplaceAll(c, "-", "")
		if len(digits) != 6 || strings.Trim(digits, "0123456789") != "" {
			return nil, fmt.Errorf("join URI code %q is not six digits", c)
		}
		spec.Code = digits
	}
	return spec, nil
}

// URI renders the spec back to the wire form — the other half of
// `panda nodes invite --uri`, so what one node prints is exactly what the
// other parses.
func (s *JoinSpec) URI() string {
	q := url.Values{}
	for _, b := range s.Bootstraps {
		q.Add("b", b)
	}
	if s.Introducer != "" {
		q.Add("k", stableIDPrefix+s.Introducer)
	}
	if s.Code != "" {
		q.Add("c", s.Code)
	}
	return "panda://join?" + q.Encode()
}

// joinCodeMatches compares the expected six-digit code with a derived SAS,
// which carries its digits as "123-456".
func joinCodeMatches(expected, sas string) bool {
	return expected == strings.ReplaceAll(sas, "-", "")
}

// ErrJoinWrongIntroducer marks a contact point that answered with an
// identity other than the one the URI pinned. It is not necessarily an
// attack — any member may be listed as a contact point — so the caller may
// try the next bootstrap; only a code mismatch is a stop.
var ErrJoinWrongIntroducer = errors.New("join: contact point is not the pinned introducer")

// ErrJoinCodeMismatch marks a derived pairing code that does not match the
// URI's c= value. This is the MITM detection firing: the DH exchange
// completed but the far end is not the party whose code the operator was
// given, so no secret may move.
var ErrJoinCodeMismatch = errors.New("join: pairing code mismatch")

// IntroducerMatches reports whether a responder's identity is the one the
// URI pinned. Unpinned means every responder matches.
func (s *JoinSpec) IntroducerMatches(pub string) bool {
	return s.Introducer == "" || strings.EqualFold(pub, s.Introducer)
}

// CodeMatches reports whether a derived pairing code matches the URI's c=
// value. Unpinned means it matches — the operator then compares by eye.
func (s *JoinSpec) CodeMatches(sas string) bool {
	return s.Code == "" || joinCodeMatches(s.Code, sas)
}

// VerifyPeer runs both pins in one call: pub is the responder's Ed25519
// identity (hex) from pair_ready, sas the derived pairing code.
func (s *JoinSpec) VerifyPeer(pub, sas string) error {
	if !s.IntroducerMatches(pub) {
		return ErrJoinWrongIntroducer
	}
	if !s.CodeMatches(sas) {
		return ErrJoinCodeMismatch
	}
	return nil
}
