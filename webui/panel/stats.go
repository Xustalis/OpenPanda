// SPDX-License-Identifier: AGPL-3.0-or-later

package panel

// Usage statistics for the profile page (GET /api/stats).
//
// Three stores answer three different questions, and the page needs all of
// them on one axis (the local calendar day):
//
//   - chat sessions: JSON files under the sessions store — sessions per day,
//     and one message per stored turn;
//   - the task queue: delegation-free rows in `tasks`, bucketed by the same
//     local day/hour the CLI heatmap uses;
//   - token spend: `delegation_metrics`, which both the commander model and
//     adapter delegations write into.
//
// The aggregation is a pure function over those inputs so the calendar's
// arithmetic (which day a turn belongs to, what "peak hour" means, which
// model won) can be tested without a database, an HTTP round-trip or a
// clock. Day boundaries are the *server's* local time throughout: the same
// rule internal/core uses for the CLI heatmap, so `panda heatmap` and this
// page never disagree about which day a task belongs to.

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Xustalis/OpenPanda/internal/core"
	"github.com/Xustalis/OpenPanda/internal/sessions"
)

// dayLayout is the calendar key. Sorting these strings sorts the days.
const dayLayout = "2006-01-02"

// statsDayJSON is one day of activity. Only days with activity are sent —
// the console paints the empty ones itself (it owns the 26-week grid), and
// shipping 182 zero rows to say "nothing happened" would be noise on the
// wire for every install that has been running longer than a week.
type statsDayJSON struct {
	Date     string `json:"date"`
	Sessions int    `json:"sessions"`
	Messages int    `json:"messages"`
	Tasks    int    `json:"tasks"`
	Tokens   int64  `json:"tokens"`
	// Models splits the day's tokens by model. The console needs it to answer
	// "favourite model in the last 30 days" — the top-level field can only
	// answer it for all time. Omitted on days whose calls reported no usage.
	Models map[string]int64 `json:"models,omitempty"`
}

// statsJSON is the wire form of GET /api/stats.
type statsJSON struct {
	Sessions      int            `json:"sessions"`
	Messages      int            `json:"messages"`
	Tasks         int            `json:"tasks"`
	Tokens        int64          `json:"tokens"`
	ActiveDays    int            `json:"active_days"`
	TotalDays     int            `json:"total_days"`
	PeakHour      *int           `json:"peak_hour"` // 0–23, null with no activity
	FavoriteModel string         `json:"favorite_model,omitempty"`
	FirstDay      string         `json:"first_day,omitempty"`
	LastDay       string         `json:"last_day,omitempty"`
	Daily         []statsDayJSON `json:"daily"`
	// SkippedRows is a health number, not a usage one: task rows every listing
	// had to drop because the driver could not decode them. Omitted when zero
	// so a healthy install's payload does not change. It rides here because
	// the alternative — "the board quietly shows fewer tasks than the table
	// holds" — is indistinguishable from a UI bug.
	SkippedRows int64 `json:"skipped_rows,omitempty"`
}

// getStats serves GET /api/stats — the profile page's activity calendar and
// its six headline numbers. Every collaborator degrades: no session store,
// no task store, or no metrics table each leave their slice empty instead of
// failing the request, because a node that has never queued a task still has
// a profile worth showing.
func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	var list []*sessions.Session
	if h.sessions != nil {
		all, err := h.sessions.List()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		list = all
	}

	var tasks []core.TaskDayHour
	var metrics []core.DelegationMetric
	if h.store != nil {
		rows, err := h.store.TaskActivityByDayHour(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		tasks = rows
		ms, err := h.store.ListDelegationMetrics(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		metrics = ms
	}

	fallbackModel := ""
	if h.cfg != nil {
		fallbackModel = h.cfg.Model.Model
	}
	out := aggregateUsage(list, tasks, metrics, fallbackModel)
	if h.store != nil {
		out.SkippedRows = h.store.SkippedRows()
	}
	writeJSON(w, out)
}

// aggregateUsage folds the three sources onto one calendar. See the package
// comment for why each input is what it is.
func aggregateUsage(
	list []*sessions.Session,
	tasks []core.TaskDayHour,
	metrics []core.DelegationMetric,
	fallbackModel string,
) statsJSON {
	days := make(map[string]*statsDayJSON)
	hourCounts := [24]int{}
	modelTokens := make(map[string]int64)

	at := func(key string) *statsDayJSON {
		d := days[key]
		if d == nil {
			d = &statsDayJSON{Date: key}
			days[key] = d
		}
		return d
	}
	localDay := func(t time.Time) string { return t.Local().Format(dayLayout) }

	for _, s := range list {
		if s == nil {
			continue
		}
		created := s.CreatedAt.Local()
		at(created.Format(dayLayout)).Sessions++
		hourCounts[created.Hour()]++

		for _, turn := range s.Turns {
			// New transcripts carry a per-turn stamp. Older ones only have
			// the session's own bounds, so a user turn lands on the day the
			// thread opened and an answer on the day it was last touched —
			// the closest reconstruction the old shape allows.
			var when time.Time
			switch {
			case turn.TS > 0:
				when = time.Unix(turn.TS, 0).Local()
			case turn.Role == "user":
				when = created
			default:
				when = s.UpdatedAt.Local()
			}
			at(when.Format(dayLayout)).Messages++
			hourCounts[when.Hour()]++
		}
	}

	for _, t := range tasks {
		if t.Day == "" || t.Count <= 0 {
			continue
		}
		at(t.Day).Tasks += t.Count
		if t.Hour >= 0 && t.Hour < 24 {
			hourCounts[t.Hour] += t.Count
		}
	}

	for _, m := range metrics {
		if !m.Tokens.Valid || m.Tokens.Int64 <= 0 || m.CreatedAt <= 0 {
			continue
		}
		label := modelLabel(m.Executor)
		d := at(localDay(time.Unix(m.CreatedAt, 0)))
		d.Tokens += m.Tokens.Int64
		if d.Models == nil {
			d.Models = make(map[string]int64, 1)
		}
		d.Models[label] += m.Tokens.Int64
		modelTokens[label] += m.Tokens.Int64
	}

	out := statsJSON{Daily: make([]statsDayJSON, 0, len(days))}
	peak := 0
	for _, d := range days {
		out.Daily = append(out.Daily, *d)
		out.Sessions += d.Sessions
		out.Messages += d.Messages
		out.Tasks += d.Tasks
		out.Tokens += d.Tokens
	}
	sort.Slice(out.Daily, func(i, j int) bool { return out.Daily[i].Date < out.Daily[j].Date })
	out.ActiveDays = len(out.Daily)
	if out.ActiveDays > 0 {
		out.FirstDay = out.Daily[0].Date
		out.LastDay = out.Daily[len(out.Daily)-1].Date
		if first, err := time.Parse(dayLayout, out.FirstDay); err == nil {
			if last, err := time.Parse(dayLayout, out.LastDay); err == nil {
				// Inclusive span: a single day of activity is one day, not zero.
				out.TotalDays = int(last.Sub(first).Hours()/24) + 1
			}
		}
	}

	// Peak hour is the busiest hour of the day across every activity kind.
	// Ties go to the earliest hour so the number is stable between requests
	// rather than depending on map iteration order.
	for h, n := range hourCounts {
		if n > hourCounts[peak] {
			peak = h
		}
	}
	if hourCounts[peak] > 0 {
		h := peak
		out.PeakHour = &h
	}

	out.FavoriteModel = favoriteModel(modelTokens)
	if out.FavoriteModel == "" {
		out.FavoriteModel = fallbackModel
	}
	return out
}

// favoriteModel returns the model that spent the most tokens, or "" when the
// ledger has nothing to say. Ties break on the name so the card does not
// flicker between two equally used models.
func favoriteModel(tokens map[string]int64) string {
	best, bestN := "", int64(0)
	for name, n := range tokens {
		if n > bestN || (n == bestN && name < best) {
			best, bestN = name, n
		}
	}
	return best
}

// modelLabel turns a delegation metric's executor into a model name. The
// entry (commander) model writes "entry:<model>" so its rows stay
// distinguishable from adapter runs in the ledger; the profile card shows
// models, not ledger bookkeeping.
func modelLabel(executor string) string {
	return strings.TrimPrefix(strings.TrimSpace(executor), "entry:")
}
