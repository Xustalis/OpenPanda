// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"errors"
	"fmt"
	"io"
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
	// A corrupted or untrusted daily path must not make one sweep read an
	// unbounded file into memory. Oversized logs fail visibly, not partially.
	maxDailyDreamBytes = 2 << 20
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
// clear the threshold + provenance gates into MEMORY.md. Profile facts go to USER.md and reusable environment knowledge to MEMORY.md.
type Dreamer struct {
	hermes     *Hermes
	now        func() time.Time // injectable clock for tests
	minScore   float64
	minRecall  int
	minQueries int
	// OnPromotion, when set, is invoked for every entry promoted into
	// the personal store (A4 visibility): the daemon records an audit/event
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
// report. Only admitted long-term facts can be promoted.
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
	release, err := lockRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("memory: lock daily scan: %w", err)
	}
	defer release()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memory: read daily dir: %w", err)
	}

	byText := make(map[string]*Candidate)
	for _, e := range entries {
		info, err := e.Info() // Lstat even when the filesystem lacks d_type
		if err != nil {
			return nil, fmt.Errorf("memory: inspect %s: %w", e.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue // archive/, symlinks, and special files are not daily logs
		}
		date, ok := parseDailyName(e.Name())
		if !ok || date.Format(dailyLayout) > d.now().Format(dailyLayout) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("memory: open %s: %w", e.Name(), err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxDailyDreamBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, fmt.Errorf("memory: read %s: %w", e.Name(), readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("memory: close %s: %w", e.Name(), closeErr)
		}
		if len(data) > maxDailyDreamBytes {
			return nil, fmt.Errorf("memory: daily log %s exceeds %d bytes", e.Name(), maxDailyDreamBytes)
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
			}
			text = sanitizeEntry(text)
			if _, err := ClassifyFact(text, "", ""); err != nil {
				continue // recurrence cannot turn operational noise into knowledge
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
		out = append(out, c)
	}
	// Deterministic order: count first (strongest signal), text as the
	// tiebreak so equal-count candidates promote in a stable sequence —
	// map iteration order must never decide which fact lands first.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Text < out[j].Text
	})
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

// deep admits durable facts before applying the six ranking signals and
// provenance gates. Profile facts go to USER.md; environment/convention facts
// go to MEMORY.md. Existing assertions win over weaker historical candidates;
// updates use the memory tools with stable keys. Over-limit promotions skip.
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
	now := d.now()
	// A newer observation of an attribute suppresses an older value, even
	// when that older value has more repetitions. Ties with different values
	// are ambiguous and are left for an explicit memory-tool correction.
	latest := map[string]*Candidate{}
	ambiguous := map[string]bool{}
	for _, c := range candidates {
		fact, err := ClassifyFact(c.Text, "", "")
		if err != nil || len(c.Days) == 0 || fact.Target() == targetProject {
			continue
		}
		_, last := dayRange(c.Days)
		if last.Format(dailyLayout) > now.Format(dailyLayout) {
			continue
		}
		if previous := latest[fact.ID()]; previous != nil {
			_, prevLast := dayRange(previous.Days)
			if last.Before(prevLast) {
				continue
			}
			if last.Equal(prevLast) {
				prevFact, _ := ClassifyFact(previous.Text, "", "")
				ambiguous[fact.ID()] = ambiguous[fact.ID()] || prevFact.Text != fact.Text
				continue
			}
		}
		latest[fact.ID()] = c
		ambiguous[fact.ID()] = false
	}

	type promotion struct {
		entry     string
		whitelist bool
	}
	var announce []promotion
	err := d.hermes.updatePersonalFiles(0, func(files []personalFile) error {
		for _, c := range candidates {
			if len(c.Days) == 0 {
				continue
			}
			fact, classifyErr := ClassifyFact(c.Text, "", "")
			destination := 1
			if fact.Target() == targetUser {
				destination = 0
			}
			mem := &files[destination].mem
			first, last := dayRange(c.Days)
			score := scoreCandidate(noveltySignal(c.Text, mem.Entries), frequencySignal(c.Count), queryDiversitySignal(len(c.Days)), recencySignal(last, now), consolidationSignal(first, last), conceptualSignal(c.Text))
			trusted := Sources(c.Sources).Trusted()
			if !trusted {
				score *= dailyExternalDiscount
			}
			c.Score = score
			if classifyErr != nil || fact.Target() == targetProject || latest[fact.ID()] != c || ambiguous[fact.ID()] {
				continue
			}
			if c.Count < d.minRecall || len(c.Days) < d.minQueries {
				continue
			}
			if trusted && score < d.minScore {
				continue
			}
			if !trusted && trustedDaysWithin(c, now, emphasizeWindowDays*24*time.Hour) < emphasizeMinDays {
				continue
			}
			exists := false
			for _, file := range files {
				for _, entry := range file.mem.Entries {
					prior, e := ClassifyFact(entry, "", "")
					if (e == nil && prior.ID() == fact.ID()) || strings.Contains(entry, fact.Text) {
						exists = true
						break
					}
				}
				if exists {
					break
				}
			}
			if exists {
				continue
			}
			entry := fact.Entry()
			if !trusted {
				entry += " " + PromotionSourceTag
			}
			if err := mem.Add(entry); err != nil {
				if errors.Is(err, ErrOverLimit) || errors.Is(err, ErrDuplicate) || errors.Is(err, ErrSeparator) {
					continue
				}
				return err
			}
			announce = append(announce, promotion{entry, !trusted})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var promoted []string
	for _, p := range announce {
		promoted = append(promoted, p.entry)
		if d.OnPromotion != nil {
			d.OnPromotion(p.entry, p.whitelist)
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
	// Filenames describe local calendar days, not UTC instants. Normalize
	// the current local date to the same representation as parseDailyName.
	dayNow, _ := time.Parse(dailyLayout, now.Format(dailyLayout))
	cutoff := dayNow.Add(-window)
	var days []time.Time
	for _, s := range c.Sources {
		if !s.Trusted {
			continue
		}
		date, ok := parseDailyName(s.Path)
		if !ok || date.Before(cutoff) || date.After(dayNow) {
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
