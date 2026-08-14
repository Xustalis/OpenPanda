package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Lifecycle windows (design §8.5): an active skill that goes unused for
// dormantAfter becomes dormant; one unused for expireAfter becomes expired and
// is pruned from matching. Pending skills (awaiting approval) are untouched.
const (
	dormantAfter = 30 * 24 * time.Hour
	expireAfter  = 90 * 24 * time.Hour
)

// Advance transitions a skill's status based on how long it has been unused.
// It returns the new status and mutates sk in place so the caller can Save it.
func Advance(sk *Skill, now time.Time) Status {
	if sk.Status == StatusPending || sk.Status == StatusExpired {
		return sk.Status
	}
	idle := now.Sub(sk.LastUsed)
	switch {
	case idle >= expireAfter:
		sk.Status = StatusExpired
	case idle >= dormantAfter:
		sk.Status = StatusDormant
	default:
		sk.Status = StatusActive
	}
	return sk.Status
}

// RecordUse marks a successful (or failed) use and updates the counters so the
// lifecycle and future triggers see the latest state.
func (sk *Skill) RecordUse(success bool, now time.Time) {
	sk.UseCount++
	if success {
		sk.SuccessCount++
	}
	sk.LastUsed = now
	if sk.Status == StatusDormant {
		sk.Status = StatusActive
	}
}

// Approve promotes a pending skill to active — the human sign-off step of the
// approval flow (design §8.2). It errors if the skill is not pending.
func (s *Store) Approve(scope Scope, key, name string) error {
	sk, err := s.mustLoad(scope, key, name)
	if err != nil {
		return err
	}
	if sk.Status != StatusPending {
		return fmt.Errorf("skills: %s is %s, not pending", name, sk.Status)
	}
	sk.Status = StatusActive
	return s.Save(sk)
}

// Reject deletes a pending skill. A non-pending skill cannot be rejected, so an
// active workflow is never silently removed.
func (s *Store) Reject(scope Scope, key, name string) error {
	sk, err := s.mustLoad(scope, key, name)
	if err != nil {
		return err
	}
	if sk.Status != StatusPending {
		return fmt.Errorf("skills: %s is %s, not pending", name, sk.Status)
	}
	path, err := s.Path(sk)
	if err != nil {
		return err
	}
	return os.RemoveAll(filepath.Dir(path))
}

// mustLoad loads a skill, failing when it is absent (unlike Load, which returns
// nil for a missing skill).
func (s *Store) mustLoad(scope Scope, key, name string) (*Skill, error) {
	sk, err := s.Load(scope, key, name)
	if err != nil {
		return nil, err
	}
	if sk == nil {
		return nil, fmt.Errorf("skills: %s not found", name)
	}
	return sk, nil
}
