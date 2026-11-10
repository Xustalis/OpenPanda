// SPDX-License-Identifier: AGPL-3.0-or-later

package entry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/i18n"
)

// A textual tool call in GLM/Zhipu's template: the model writes the call into
// the content field as
//
//	<tool_call>workspace_list<arg_key>path</arg_key><arg_value>projects</arg_value></tool_call>
//
// instead of emitting a native tool_use block. Relays that cannot carry the
// tools parameter through (and models behind them) produce exactly this, and
// without a recovery path it reaches the user as an "answer" — raw markup
// where a directory listing should be — while the call the model intended
// never runs.
//
// The policy is the one dsml.go already applies to the DSML dialect: recover
// and execute when the round offered tools, strip when it did not, and never
// let the markup stand as prose. The two dialects differ only in their
// delimiters, so a surface that handles one has to handle the other: the
// failure mode is identical and the user cannot tell which template their
// endpoint chose.
const (
	xmlToolOpen  = "<tool_call>"
	xmlToolClose = "</tool_call>"
	xmlKeyOpen   = "<arg_key>"
	xmlKeyClose  = "</arg_key>"
	xmlValOpen   = "<arg_value>"
	xmlValClose  = "</arg_value>"
)

// ContainsTextualToolCall reports whether s carries tool-call markup in any
// dialect this package recovers: DSML or the GLM <tool_call> template. Every
// caller that used ContainsDSMLToolCall as its "this is markup, not an answer"
// gate should use this instead.
func ContainsTextualToolCall(s string) bool {
	return ContainsDSMLToolCall(s) || strings.Contains(s, xmlToolOpen)
}

// StripTextualToolCalls removes tool-call markup from text, keeping the prose
// around it: what preceded the first marker plus whatever follows the last
// closing tag. Text without markup is returned unchanged.
func StripTextualToolCalls(text string) string {
	out := StripDSMLToolCalls(text)
	start := strings.Index(out, xmlToolOpen)
	if start < 0 {
		return out
	}
	head := strings.TrimSpace(out[:start])
	tail := ""
	if end := strings.LastIndex(out, xmlToolClose); end >= 0 {
		tail = strings.TrimSpace(out[end+len(xmlToolClose):])
	}
	switch {
	case head != "" && tail != "":
		return head + "\n" + tail
	case head != "":
		return head
	default:
		return tail
	}
}

// parseXMLToolCalls extracts the <tool_call> blocks from text carrying GLM
// markup, returning them as ToolUses plus the prose that preceded the markup.
// ok is false when no usable call exists — the markup is then treated as
// unparsable, exactly like a broken DSML block.
//
// A block without its closing tag is still parsed: the model stopped at
// max_tokens mid-call, and the arguments it did write are enough to run it.
// Dropping it would leave the user with the same "no answer and no action"
// the recovery path exists to prevent.
func parseXMLToolCalls(text string) (uses []ToolUse, preamble string, ok bool) {
	start := strings.Index(text, xmlToolOpen)
	if start < 0 {
		return nil, "", false
	}
	preamble = strings.TrimSpace(text[:start])
	rest := text[start:]
	for {
		i := strings.Index(rest, xmlToolOpen)
		if i < 0 {
			break
		}
		body := rest[i+len(xmlToolOpen):]
		closed := false
		if end := strings.Index(body, xmlToolClose); end >= 0 {
			body = body[:end]
			rest = rest[i+len(xmlToolOpen)+end+len(xmlToolClose):]
			closed = true
		} else {
			rest = ""
		}
		if name, params, okOne := parseXMLCallBody(body); okOne {
			uses = append(uses, ToolUse{Name: name, Input: params})
		}
		if !closed {
			break
		}
	}
	return uses, preamble, len(uses) > 0
}

// parseXMLCallBody reads one <tool_call> body: a bare tool name followed by
// zero or more <arg_key>/<arg_value> pairs. The name test is strict on purpose
// — a model explaining the template in prose ("<tool_call> takes a name")
// must fail it rather than be executed as a tool.
func parseXMLCallBody(body string) (string, map[string]any, bool) {
	s := strings.TrimSpace(body)
	if s == "" {
		return "", nil, false
	}
	name := s
	params := map[string]any{}
	if i := strings.IndexByte(s, '<'); i >= 0 {
		name = strings.TrimSpace(s[:i])
		s = s[i:]
		for {
			key, rest, okKey := xmlTagText(s, xmlKeyOpen, xmlKeyClose)
			if !okKey {
				break
			}
			val, rest2, okVal := xmlTagText(rest, xmlValOpen, xmlValClose)
			if !okVal {
				break
			}
			if key = strings.TrimSpace(key); key != "" {
				params[key] = xmlParamValue(val)
			}
			s = rest2
		}
	}
	if !toolNameShape(name) {
		return "", nil, false
	}
	return name, params, true
}

// xmlTagText reads the text of the first open/close tag pair in s, tolerating
// attributes on the opening tag (relays rewrite them). It returns the text and
// the remainder after the closing tag.
func xmlTagText(s, open, close string) (string, string, bool) {
	i := strings.Index(s, open[:len(open)-1])
	if i < 0 {
		return "", s, false
	}
	gt := strings.IndexByte(s[i:], '>')
	if gt < 0 {
		return "", s, false
	}
	inner := s[i+gt+1:]
	end := strings.Index(inner, close)
	if end < 0 {
		return "", s, false
	}
	return inner[:end], inner[end+len(close):], true
}

// toolNameShape accepts the one token a tool name is made of: letters, digits,
// underscore, dot, colon, dash or slash. Registered names are ASCII, so
// anything else is prose, not a call.
func toolNameShape(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == ':' || r == '-' || r == '/':
		default:
			return false
		}
	}
	return true
}

// xmlParamValue types one argument value. GLM's template carries no type
// attribute, so the shape decides: booleans and numbers are what tools with
// flag or numeric arguments expect (boolArg asserts on the Go type and would
// read the string "true" as false), while everything else stays the string the
// model wrote.
func xmlParamValue(raw string) any {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if v[0] == '{' || v[0] == '[' {
		var parsed any
		if json.Unmarshal([]byte(v), &parsed) == nil {
			return parsed
		}
		return v
	}
	var num float64
	if err := json.Unmarshal([]byte(v), &num); err == nil {
		return num
	}
	return v
}

// resolveXMLToolCall routes a response whose tool call arrived in GLM's
// textual template. The contract mirrors resolveDSML: with tools offered, a
// parseable call is recovered into a KindToolCall output and executed through
// the registry like any native tool_use; in a tool-free round the markup is
// stripped and the surrounding prose kept, and a markup-only response is
// rejected with an actionable error rather than shown raw.
func resolveXMLToolCall(resp Response, toolsOffered bool, locs ...i18n.Locale) (Output, error) {
	loc := i18n.Locale("")
	if len(locs) > 0 {
		loc = locs[0]
	}
	uses, preamble, ok := parseXMLToolCalls(resp.Text)
	if ok && toolsOffered {
		out := Output{Kind: KindToolCall, Note: preamble}
		for _, u := range uses {
			out.Tools = append(out.Tools, &ToolCall{Tool: u.Name, Arguments: u.Input})
		}
		out.Tool = out.Tools[0]
		return out, nil
	}
	if ok {
		if preamble != "" {
			return Output{Kind: KindAnswer, Answer: preamble}, nil
		}
		return Output{}, &ClassifyError{
			UserMsg: i18n.T(loc, "entry.err.dsmlRejected"),
			Err:     fmt.Errorf("entry: textual tool call %q in a tool-free round", uses[0].Name),
		}
	}
	// Unparsable markup: strip it and let the normal parser try the rest.
	if stripped := strings.TrimSpace(StripTextualToolCalls(resp.Text)); stripped != "" {
		if out, err := ParseOutput(stripped); err == nil {
			note := i18n.T(loc, "entry.note.dsmlIgnored")
			if out.Note == "" {
				out.Note = note
			} else {
				out.Note += " " + note
			}
			return out, nil
		}
	}
	return Output{}, &ClassifyError{
		UserMsg: i18n.T(loc, "entry.err.dsmlUnparsable"),
		Err:     fmt.Errorf("entry: unparsable textual tool-call markup: %s", dsmlSample(resp.Text)),
	}
}

// ParseTextualToolCalls exposes the recovery parser to surfaces that keep
// stored transcripts: an answer written before the recovery path existed was
// persisted as raw markup, and re-rendering it verbatim is the same leak the
// parser now prevents. It returns the call names in order and the prose around
// them; ok is false when the text carries no usable call.
func ParseTextualToolCalls(text string) (names []string, prose string, ok bool) {
	if ContainsDSMLToolCall(text) {
		if uses, pre, okDSML := parseDSMLToolCalls(text); okDSML {
			for _, u := range uses {
				names = append(names, u.Name)
			}
			return names, pre, true
		}
	}
	if strings.Contains(text, xmlToolOpen) {
		if uses, pre, okXML := parseXMLToolCalls(text); okXML {
			for _, u := range uses {
				names = append(names, u.Name)
			}
			return names, pre, true
		}
	}
	return nil, strings.TrimSpace(text), false
}

// xmlToolCallStartIndex is the stream guard's detector: markup at the very
// start of a response is withheld like any other structured output. A marker
// in the middle of prose is left alone — unlike DSML's private delimiters,
// "<tool_call>" is ordinary text in a conversation about tool protocols, and
// swallowing the rest of a real answer is the worse failure.
func xmlToolCallStartIndex(s string) int {
	return strings.Index(s, xmlToolOpen)
}

// xmlToolCallPrefix reports whether s is a proper prefix of the opening tag —
// a tag split across deltas is still undecidable, so the guard holds it.
func xmlToolCallPrefix(s string) bool {
	return len(s) < len(xmlToolOpen) && strings.HasPrefix(xmlToolOpen, s)
}
