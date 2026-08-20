package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Deep-phase thresholds, matching OpenClaw's documented defaults (design
// §17.3): a candidate must clear minScore, minRecallCount and minUniqueQueries
// before it is eligible for promotion.
const (
	defaultMinScore   = 0.8
	defaultMinRecall  = 3
	defaultMinQueries = 3
	// minCandidateChars drops daily-log lines too short to carry a durable fact.
	minCandidateChars = 10
)

const (
	// dailyExternalDiscount is the A4 "logs are low-weight" factor: candidates
	// whose provenance carries external daily-log lines ([ext], user/wire text)
	// are weak evidence, so their Deep score is halved before the threshold
	// gates. Agent-observed (trusted) candidates keep full weight.
	dailyExternalDiscount = 0.5
	// PromotionSourceTag marks entries promoted through the repeated-emphasis
	// whitelist channel, so the user can tell log-derived memory in MEMORY.md
	// and correct or delete it via the memory API.
	PromotionSourceTag = "[from:日志]"
	// emphasizeWindowDays / emphasizeMinDays define "repeated emphasis": the
	// agent itself observed the same text on at least emphasizeMinDays distinct
	// days within the last emphasizeWindowDays. That — and never external-only
	// repetition — opens the whitelist channel around the provenance gate.
	emphasizeWindowDays = 7
	emphasizeMinDays    = 3
)

// Candidate is one short-term signal consolidated out of the daily logs.
type Candidate struct {
	Text    string
	Days    []time.Time // distinct days the text appeared
	Count   int         // total occurrences
	Sources []Source
	Score   float64 // filled during Deep
}

// Dreamer runs the three-phase memory consolidation (design §17.3), aligned
// with OpenClaw: Light dedupes daily-log lines into candidates, REM groups them
// into themes, and Deep scores them on six signals and promotes the ones that
// clear the threshold + provenance gates into MEMORY.md. It never touches the
// personal USER.md.
type Dreamer struct {
	hermes     *Hermes
	now        func() time.Time // injectable clock for tests
	minScore   float64
	minRecall  int
	minQueries int
	// OnPromotion, when set, is invoked for every entry promoted into
	// MEMORY.md (A4 visibility): the daemon records an audit/event
	// (core.EvMemoryPromotion) so the console can show what the Dreaming
	// engine memorized and let the user correct or delete it. viaWhitelist
	// reports whether the entry came through the repeated-emphasis channel.
	OnPromotion func(entry string, viaWhitelist bool)
}

// NewDreamer builds a dreamer with OpenClaw's default thresholds.
func NewDreamer(hermes *Hermes) *Dreamer {
	return &Dreamer{
		hermes:     hermes,
		now:        time.Now,
		minScore:   defaultMinScore,
		minRecall:  defaultMinRecall,
		minQueries: defaultMinQueries,
	}
}

// Report summarizes one sweep for the scheduler and the DREAMS.md diary.
type Report struct {
	Candidates int
	Promoted   []string
	Themes     map[string]int // REM theme -> candidate count
}

// Dream runs one full Light→REM→Deep sweep over the warm layer and returns a
// report. Promotion writes MEMORY.md only.
func (d *Dreamer) Dream() (Report, error) {
	candidates, err := d.light()
	if err != nil {
		return Report{}, err
	}
	themes := rem(candidates)
	promoted, err := d.deep(candidates)
	if err != nil {
		return Report{}, err
	}
	return Report{Candidates: len(candidates), Promoted: promoted, Themes: themes}, nil
}

// light scans the daily logs, dedupes lines into candidates, and drops noise.
// Every candidate carries provenance (file + line) so Deep can apply the taint
// gate.
func (d *Dreamer) light() ([]*Candidate, error) {
	dir := d.hermes.WarmDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read daily dir: %w", err)
	}

	byText := make(map[string]*Candidate)
	for _, e := range entries {
		if e.IsDir() {
			continue // skip daily/archive/
		}
		date, ok := parseDailyName(e.Name())
		if !ok {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("memory: read %s: %w", e.Name(), err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			text := stripDailyPrefix(line)
			if text == "" {
				continue
			}
			// Lines carrying external input (task titles, user/wire text) are
			// tagged by Daily.AppendExternal; taint their provenance so the
			// Deep gate can never promote user-controlled text into MEMORY.md
			// (P1-22). A candidate with any untrusted source is dropped
			// wholesale by Sources.Trusted().
			trusted := true
			if rest, ok := strings.CutPrefix(text, ExternalMarker+" "); ok {
				trusted = false
				text = rest
				if text == "" {
					continue
				}
			}
			c := byText[text]
			if c == nil {
				c = &Candidate{Text: text}
				byText[text] = c
			}
			c.Count++
			c.Sources = append(c.Sources, Source{Path: e.Name(), Line: i + 1, Trusted: trusted})
			if !containsDay(c.Days, date) {
				c.Days = append(c.Days, date)
			}
		}
	}

	out := make([]*Candidate, 0, len(byText))
	for _, c := range byText {
		if len(c.Text) >= minCandidateChars {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// rem groups candidates into coarse themes by their concept tokens, producing
// the reflection summary (OpenClaw REM). It is deterministic and writes nothing
// to MEMORY.md.
func rem(candidates []*Candidate) map[string]int {
	themes := make(map[string]int)
	for _, c := range candidates {
		themes[dominantTheme(c.Text)]++
	}
	return themes
}

// deep scores each candidate on six signals and promotes the ones that clear
// the threshold and provenance gates into MEMORY.md. Promotion is additive —
// each promoted text becomes one entry, and an over-limit add is skipped (the
// agent consolidates MEMORY.md via the memory tool, per the Hermes workflow).
//
// A4 gates, in order:
//  1. A candidate carrying external daily-log sources gets its score halved
//     (dailyExternalDiscount) — logs are low-weight evidence.
//  2. Count and distinct-day minimums apply to every candidate.
//  3. Fully trusted candidates must also clear minScore.
//  4. Candidates with any untrusted source are dropped by the provenance gate
//     unless they show repeated emphasis — the agent itself observed the text
//     on >=emphasizeMinDays distinct days within the last emphasizeWindowDays.
//     Such whitelist promotions are annotated with PromotionSourceTag so the
//     user can review and correct them. External-only repetition never opens
//     the channel (stored prompt injection stays closed, P1-22).
func (d *Dreamer) deep(candidates []*Candidate) ([]string, error) {
	mem, err := d.hermes.LoadMemory()
	if err != nil {
		return nil, err
	}
	now := d.now()

	var promoted []string
	for _, c := range candidates {
		if len(c.Days) == 0 {
			continue
		}
		first, last := dayRange(c.Days)
		score := scoreCandidate(
			noveltySignal(c.Text, mem.Entries),
			frequencySignal(c.Count),
			queryDiversitySignal(len(c.Days)),
			recencySignal(last, now),
			consolidationSignal(first, last),
			conceptualSignal(c.Text),
		)
		trusted := Sources(c.Sources).Trusted()
		if !trusted {
			// A4 low weight: daily-log lines carrying external input are weak
			// evidence — discount before the threshold gates (see the weight
			// system in signals.go; this discount rides beside it).
			score *= dailyExternalDiscount
		}
		c.Score = score
		if c.Count < d.minRecall || len(c.Days) < d.minQueries {
			continue
		}
		entry := c.Text
		viaWhitelist := false
		if trusted {
			if score < d.minScore {
				continue
			}
		} else {
			// Provenance taint gate with the repeated-emphasis whitelist
			// channel: only the agent's own repeated observations open it.
			if trustedDaysWithin(c, now, emphasizeWindowDays*24*time.Hour) < emphasizeMinDays {
				continue
			}
			entry = c.Text + " " + PromotionSourceTag
			viaWhitelist = true
		}
		if err := mem.Add(entry); err != nil {
			// A candidate already promoted into MEMORY.md (and still present in
			// the warm daily logs) is not an error — skip it, don't abort the
			// whole sweep. An over-limit add likewise leaves consolidation to
			// the agent rather than failing the dream.
			if errors.Is(err, ErrOverLimit) || errors.Is(err, ErrDuplicate) {
				continue
			}
			return nil, err
		}
		promoted = append(promoted, entry)
		if d.OnPromotion != nil {
			d.OnPromotion(entry, viaWhitelist)
		}
	}

	if len(promoted) > 0 {
		if err := d.hermes.SaveMemory(mem); err != nil {
			return nil, err
		}
	}
	return promoted, nil
}

// trustedDaysWithin counts the distinct days a candidate was observed by a
// trusted source within [now-window, now] — the "repeated emphasis" measure
// that opens the whitelist promotion channel. External sightings never count:
// user/wire text repeating itself is exactly the injection pattern the
// provenance gate exists to stop.
func trustedDaysWithin(c *Candidate, now time.Time, window time.Duration) int {
	cutoff := now.Add(-window)
	var days []time.Time
	for _, s := range c.Sources {
		if !s.Trusted {
			continue
		}
		date, ok := parseDailyName(s.Path)
		if !ok || date.Before(cutoff) {
			continue
		}
		if !containsDay(days, date) {
			days = append(days, date)
		}
	}
	return len(days)
}

// stripDailyPrefix removes the "- 15:04:05 " prefix Daily.Append writes.
func stripDailyPrefix(line string) string {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "- ")
	if len(s) >= 8 && s[2] == ':' && s[5] == ':' {
		s = strings.TrimSpace(s[8:])
	}
	return s
}

// containsDay reports whether days contains date (linear scan — day counts are
// small and bounded by the retention window).
func containsDay(days []time.Time, date time.Time) bool {
	for _, d := range days {
		if d.Equal(date) {
			return true
		}
	}
	return false
}

// dayRange returns the earliest and latest sighting day.
func dayRange(days []time.Time) (first, last time.Time) {
	first, last = days[0], days[0]
	for _, d := range days[1:] {
		if d.Before(first) {
			first = d
		}
		if d.After(last) {
			last = d
		}
	}
	return first, last
}

// dominantTheme returns a deterministic concept token to group a candidate
// under, or the "general" bucket when it has none.
func dominantTheme(text string) string {
	best := ""
	for w := range tokenize(text) {
		if isConceptToken(w) && (best == "" || w < best) {
			best = w
		}
	}
	if best == "" {
		return "general"
	}
	return best
}
