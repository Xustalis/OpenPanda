package skills

import "sync"

// Tracker aggregates task execution history per class and, when a class clears
// the create gate, generates a pending skill (design §8.2). It is the wiring
// that turns task history into procedural memory: the trigger (ShouldCreate)
// and generator (Generate) both feed off the history it accumulates.
type Tracker struct {
	store   *Store
	mu      sync.Mutex
	history map[string][]Record
}

// NewTracker builds a tracker over a skill store. History is in-memory — skill
// generation is a low-frequency event, and a restart simply resets the running
// aggregate (acceptable for the MVP; persistent history can be a later table).
func NewTracker(store *Store) *Tracker {
	return &Tracker{store: store, history: make(map[string][]Record)}
}

// Record observes one task execution. When its class now clears the create gate,
// it generates and persists a pending skill and returns it (so the caller can
// log or report). Otherwise it returns (nil, nil).
func (t *Tracker) Record(project string, abilities []string, title string, success bool) (*Skill, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	class := ClassKey(abilities)
	if class == "" {
		return nil, nil // no abilities to key a class on; nothing to learn
	}
	recs := append(t.history[class], Record{Project: project, Title: title, Success: success})
	t.history[class] = recs

	var stats Stats
	for _, r := range recs {
		stats.Attempts++
		if r.Success {
			stats.Successes++
		}
	}

	scope, key := ScopeGlobal, ""
	if project != "" {
		scope, key = ScopeProject, project
	}
	name := slug(class)
	if !ShouldCreate(stats, t.exists(scope, key, name)) {
		return nil, nil
	}

	sk := Generate(scope, key, class, describe(recs), recs)
	if err := t.store.Save(sk); err != nil {
		return nil, err
	}
	return sk, nil
}

// exists reports whether a skill of this name already exists in the same scope
// (and, for project/device scopes, the same key), so a class is distilled once
// per scope rather than suppressed globally. A project skill never blocks the
// same-named skill in another project (design §8.3).
func (t *Tracker) exists(scope Scope, key, name string) bool {
	index, err := t.store.Index()
	if err != nil {
		return false
	}
	for _, e := range index {
		if e.Name == name && e.Scope == scope && e.Key == key {
			return true
		}
	}
	return false
}

// describe returns the most recent successful title as the skill description,
// falling back to the last title when none succeeded.
func describe(recs []Record) string {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Success {
			return recs[i].Title
		}
	}
	return recs[len(recs)-1].Title
}
