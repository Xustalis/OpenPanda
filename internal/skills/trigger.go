package skills

// Stats aggregates one task class's execution history for the create trigger.
type Stats struct {
	Attempts  int // times this task class ran
	Successes int // of those, how many succeeded
}

// successRate returns the class's success rate in [0,1]; 0 when it never ran.
func (s Stats) successRate() float64 {
	if s.Attempts == 0 {
		return 0
	}
	return float64(s.Successes) / float64(s.Attempts)
}

// ShouldCreate reports whether a task class warrants a new skill, using the
// MUSE quality gate (design §8.2): the class ran >=3 times with >=70% success.
// It is suppressed when an equivalent skill already exists, so a skill is
// created once and then patched on discovery rather than duplicated.
//
// Hermes's alternative single-task trigger (>=5 tool calls) is deliberately
// omitted: OpenPanda's agent runs as a subprocess, so the core cannot observe
// the agent's internal tool-call count. The aggregate gate is the one
// OpenPanda can actually feed from its task history.
func ShouldCreate(stats Stats, exists bool) bool {
	if exists || stats.Successes == 0 {
		return false
	}
	return stats.Attempts >= 3 && stats.successRate() >= 0.7
}
