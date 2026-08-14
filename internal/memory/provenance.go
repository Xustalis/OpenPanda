package memory

// Source records where a memory candidate came from (OpenClaw provenance,
// design §17.3). A Trusted source is the agent's own daily log; untrusted or
// system sources are excluded from promotion by the provenance gate.
type Source struct {
	Path    string // daily log filename, e.g. "2026-08-13.md"
	Line    int    // 1-based line number
	Trusted bool   // whether this origin may be promoted into MEMORY.md
}

// Sources is a candidate's provenance list.
type Sources []Source

// Trusted reports whether the candidate has at least one source and every
// source is trusted. OpenClaw removes untrusted/system provenance before
// consolidation — a structural taint gate, not a score penalty — so a candidate
// with any untrusted origin is dropped wholesale rather than partially trusted.
func (s Sources) Trusted() bool {
	if len(s) == 0 {
		return false
	}
	for _, src := range s {
		if !src.Trusted {
			return false
		}
	}
	return true
}
