// SPDX-License-Identifier: AGPL-3.0-or-later

package memory

import (
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/security"
)

// ErrNotDurable rejects uncertain, transient or unclassified model writes.
// File editors can still maintain legacy prose; Facts excludes it from graphs.
var ErrNotDurable = errors.New("memory: entry is not a classified long-term fact")

// Fact is an atomic, reusable assertion. Key identifies the attribute, never its
// current value (user.name, preference.theme, build.command, etc.). Keys are
// scoped to a file/project; two projects cannot replace one another's facts.
type Fact struct {
	Category string
	Key      string
	Text     string
}

// ID includes the category so unrelated attributes cannot collide across types.
func (f Fact) ID() string { return f.Category + ":" + f.Key }

func (f Fact) Entry() string {
	return fmt.Sprintf("[fact:%s:%s] %s", f.Category, f.Key, f.Text)
}

func (f Fact) Target() string {
	switch f.Category {
	case "identity", "preference", "personal":
		return targetUser
	case "project":
		return targetProject
	default:
		return targetMemory
	}
}

var factHeader = regexp.MustCompile(`^\[fact:([a-z]+):([a-z0-9][a-z0-9_.-]{0,95})\] (.+)$`)
var factKey = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,95}$`)
var transientFact = regexp.MustCompile(`(?i)(今天|明天|昨天|今晚|这次|本次|此次|临时|一次性|暂时|刚才|稍后|待办|任务总结|构建成功|测试通过|任务完成|任务失败|会议安排|天气|假设|也许|可能|扮演|例如|比如|[?？]|\b(today|tomorrow|yesterday|tonight|temporary|temporarily|one-off|this (time|session|task)|task (completed|failed|summary)|build (passed|succeeded)|tests? passed|todo|maybe|perhaps|pretend|for example)\b)`)
var personalSubject = regexp.MustCompile(`(?i)^(用户|我|user\b|i\b|my\b)`)

type factRule struct {
	pattern       *regexp.Regexp
	category, key string
}

// These deliberately narrow legacy rules recognize assertions, not words
// mentioned anywhere in a task report. Other durable facts need category/key
// from the model, or an explicit [fact:category:key] entry from a file editor.
var factRules = []factRule{
	{regexp.MustCompile(`(?i)^(用户(的)?姓名[：:是]|我(的名字|叫|名叫)|my name is |user('?s)? name (is|:) |姓名[：:])`), "identity", "user.name"},
	{regexp.MustCompile(`(?i)^(用户(的)?(职业|工作是)|我是.{1,30}(工程师|教师|医生|设计师)|user works as |职业[：:])`), "identity", "user.occupation"},
	{regexp.MustCompile(`(?i)^(用户(的)?(常住地|居住地)|我(住在|定居)|user lives in |常住地[：:])`), "personal", "user.location"},
	{regexp.MustCompile(`(?i)^(用户(的)?时区|我(的)?时区|user('?s)? timezone|timezone[：:])`), "personal", "user.timezone"},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(过敏|allergic to)`), "personal", ""},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(偏好|喜欢|习惯|prefer).{0,12}(深色|浅色|暗色|亮色|dark mode|light mode)`), "preference", "preference.theme"},
	{regexp.MustCompile(`(?i)^prefers? (dark|light) mode`), "preference", "preference.theme"},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(偏好|喜欢|习惯|prefer).{0,15}(中文|英文|英语|chinese|english)`), "preference", "preference.language"},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(偏好|喜欢|习惯|prefer).{0,12}(喝|咖啡|茶|coffee|tea)`), "preference", "preference.drink"},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(偏好|喜欢|习惯|prefer).{0,15}(简洁|详细|concise|detailed)`), "preference", "preference.detail"},
	{regexp.MustCompile(`(?i)^(用户|我|user).{0,12}(偏好|喜欢|习惯|prefer)`), "preference", ""},
	{regexp.MustCompile(`(?i)^(server (is|runs) |服务器(系统|运行|使用)|操作系统[：:])`), "environment", "environment.os"},
	{regexp.MustCompile(`(?i)^(全局约定|通用约定|global convention)[：:]`), "convention", ""},
	{regexp.MustCompile(`(?i)^(项目(约定|架构|使用|采用|构建|测试)|project (uses|convention|architecture)|build command[：:]|test command[：:])`), "project", ""},
}

// ClassifyFact is shared by writes, dreams and graph projection. An explicit
// category is evidence of classification, not a bypass for transient content.
// Inference is conservative; uncertain input stays in daily/session history.
func ClassifyFact(text, category, key string) (Fact, error) {
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), PromotionSourceTag))
	if text == "" || strings.ContainsAny(text, "\r\n§") || strings.HasPrefix(text, ExternalMarker) {
		return Fact{}, ErrNotDurable
	}
	if m := factHeader.FindStringSubmatch(text); m != nil {
		if (category != "" && category != m[1]) || (key != "" && key != m[2]) {
			return Fact{}, fmt.Errorf("%w: conflicting fact metadata", ErrNotDurable)
		}
		category, key, text = m[1], m[2], m[3]
	} else if strings.HasPrefix(text, "[fact:") {
		return Fact{}, fmt.Errorf("%w: malformed fact metadata", ErrNotDurable)
	}
	// Recheck assertion content after removing metadata; a header must never
	// hide external provenance or another fact record.
	if strings.TrimSpace(text) == "" || transientFact.MatchString(text) || security.Redact(text) != text || strings.HasPrefix(text, ExternalMarker) || strings.HasPrefix(text, "[fact:") {
		return Fact{}, ErrNotDurable
	}
	if personalSubject.MatchString(text) && (category == "environment" || category == "convention" || category == "project") {
		return Fact{}, ErrNotDurable
	}
	if category == "" && key == "" {
		for _, rule := range factRules {
			if rule.pattern.MatchString(text) {
				category, key = rule.category, rule.key
				if key == "" {
					key = literalFactKey(text)
				}
				break
			}
		}
	}
	for _, rule := range factRules {
		if rule.pattern.MatchString(text) {
			if rule.category == "project" && category != "project" {
				return Fact{}, ErrNotDurable
			}
			if (rule.category == "identity" || rule.category == "preference" || rule.category == "personal") && rule.category != category {
				return Fact{}, ErrNotDurable
			}
			if rule.category == category && rule.key != "" {
				// Preserve explicit scopes such as environment.server_a.os.
				// Common short aliases normalize to their canonical attribute.
				short := rule.key[strings.LastIndex(rule.key, ".")+1:]
				if key == "" || key == short {
					key = rule.key
				}
			}
			break
		}
	}
	switch category {
	case "identity", "preference", "personal", "environment", "convention", "project":
	default:
		return Fact{}, fmt.Errorf("%w: supply category and stable key", ErrNotDurable)
	}
	if !factKey.MatchString(key) {
		return Fact{}, fmt.Errorf("%w: invalid or missing stable key", ErrNotDurable)
	}
	if category == "project" && personalSubject.MatchString(text) {
		return Fact{}, fmt.Errorf("%w: personal facts do not belong to project memory", ErrNotDurable)
	}
	return Fact{Category: category, Key: key, Text: text}, nil
}

func literalFactKey(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(sanitizeEntry(text))))
	return fmt.Sprintf("note.%x", h.Sum64())
}

// Facts projects legacy and structured entries without rewriting or deleting
// source data. For duplicate keys, the last retained assertion wins.
func Facts(m MemFile, target string) []Fact {
	var facts []Fact
	indices := map[string]int{}
	for _, entry := range m.Entries {
		fact, err := ClassifyFact(entry, "", "")
		if err != nil || (target == "project" && fact.Target() != targetProject) || (target != "project" && fact.Target() == targetProject) {
			continue
		}
		if idx, ok := indices[fact.ID()]; ok {
			facts[idx] = fact
		} else {
			indices[fact.ID()] = len(facts)
			facts = append(facts, fact)
		}
	}
	return facts
}

// UpsertFact replaces matching attributes atomically inside the store's Update
// callback, collapses legacy duplicates, and rolls back on a capacity failure.
// No fuzzy text similarity is used: unrelated facts must never overwrite one
// another merely because they share words.
func (m *MemFile) UpsertFact(fact Fact) (bool, error) {
	checked, err := ClassifyFact(fact.Entry(), "", "")
	if err != nil {
		return false, err
	}
	fact = checked
	entries := make([]string, 0, len(m.Entries)+1)
	found := false
	for _, entry := range m.Entries {
		old, err := ClassifyFact(entry, "", "")
		if err == nil && old.ID() == fact.ID() {
			if !found {
				entries = append(entries, fact.Entry())
				found = true
			}
		} else {
			entries = append(entries, entry)
		}
	}
	if !found {
		entries = append(entries, fact.Entry())
	}
	next := MemFile{Entries: entries, Limit: m.Limit}
	if next.Limit > 0 && next.Chars() > next.Limit {
		return false, ErrOverLimit
	}
	if string(next.Bytes()) == string(m.Bytes()) {
		return false, nil
	}
	*m = next
	return true, nil
}

// matchAssertion excludes metadata from substring selection.
func matchAssertion(m MemFile, sub string) (int, bool) {
	if strings.TrimSpace(sub) == "" {
		return -1, false
	}
	idx := -1
	for i, entry := range m.Entries {
		text := entry
		if fact, err := ClassifyFact(entry, "", ""); err == nil {
			text = fact.Text
		}
		if strings.Contains(text, sub) {
			if idx >= 0 {
				return -1, false
			}
			idx = i
		}
	}
	return idx, idx >= 0
}
