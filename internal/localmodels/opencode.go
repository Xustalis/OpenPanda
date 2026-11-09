// SPDX-License-Identifier: AGPL-3.0-or-later

package localmodels

// OpenCode is the one source that has to be asked, not read: it keeps its
// provider registry inside the app, so `opencode models` is the only way to see
// what it can serve.
//
// `--verbose` is the important half. The plain list is `provider/model` and
// nothing else, which tempts a caller into mapping the namespace onto a
// registry provider by name — and that is wrong for the one case that matters
// here: OpenCode's `alibaba` and `alibaba-cn` are two different services
// (dashscope-intl vs dashscope), so folding both into one provider writes half
// the rows against an endpoint their key cannot reach. The verbose form carries
// each model's own `api.url`, which is the only honest answer to "where does
// this id live".
//
// Two things this collector deliberately does not do:
//
//   - It does not treat the catalogue as proof of access. `opencode models`
//     prints the whole built-in list whether or not a key is configured; a
//     model listed there fails on the first turn without credentials. Every row
//     is unsuggested, and importability is decided by probing the model's own
//     endpoint with the provider's key.
//   - It does not read OpenCode's credential store. Newer builds moved
//     credentials from auth.json into opencode.db, and the app owns that file;
//     what matters here is only whether a provider's environment variable is
//     set, which is a question the environment can answer on its own.

import (
	"bufio"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Xustalis/OpenPanda/internal/providers"
)

// providerKeyEnv names the environment variable each OpenCode namespace
// authenticates with. Discovery only reads whether it is set; ResolveKey fetches
// the value at import time.
var providerKeyEnv = map[string]string{
	"alibaba":     "DASHSCOPE_API_KEY",
	"alibaba-cn":  "DASHSCOPE_API_KEY",
	"anthropic":   "ANTHROPIC_API_KEY",
	"deepseek":    "DEEPSEEK_API_KEY",
	"moonshot":    "MOONSHOT_API_KEY",
	"openai":      "OPENAI_API_KEY",
	"opencode":    "OPENCODE_API_KEY",
	"openrouter":  "OPENROUTER_API_KEY",
	"siliconflow": "SILICONFLOW_API_KEY",
	"zhipu":       "ZHIPU_API_KEY",
}

// ocModel is one record of `opencode models --verbose`.
type ocModel struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	API        struct {
		URL string `json:"url"`
	} `json:"api"`
	Limit struct {
		Context int `json:"context"`
	} `json:"limit"`
}

// collectOpenCode runs the CLI and groups its catalogue by (provider, endpoint).
func collectOpenCode(ctx context.Context, opt Options) Source {
	src := Source{ID: SourceOpenCode, Label: "OpenCode", Status: StatusMissing}
	cli := findCLI(opt.OpenCodeCLI, "OPENCODE_CLI", "opencode")
	if cli == "" {
		src.Detail = "opencode CLI not on PATH — set OPENCODE_CLI to its full path"
		return src
	}

	runCtx, cancel := context.WithTimeout(ctx, opt.CLITimeout)
	defer cancel()
	models, err := readOpenCodeCatalogue(runCtx, cli)
	if err != nil {
		src.Status = StatusError
		src.Detail = "opencode models: " + firstLine(err.Error())
		return src
	}

	// Group by the pair that actually decides where a request goes.
	type key struct{ provider, url string }
	order := make([]key, 0, 4)
	groups := make(map[key][]ocModel)
	for _, m := range models {
		k := key{m.ProviderID, strings.TrimRight(m.API.URL, "/")}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], m)
	}

	importable, blocked := 0, 0
	// Namespaces whose credential variable is simply not exported, tracked so
	// the refusal line can name the one variable that would unlock the sheet.
	missingEnv := make(map[string]bool)
	for _, k := range order {
		rows := groups[k]
		keyEnv := providerKeyEnv[k.provider]
		keyRef := ""
		if keyEnv != "" && opt.env(keyEnv) != "" {
			keyRef = "env:" + keyEnv
		} else if keyEnv != "" {
			missingEnv[keyEnv] = true
		}

		// Probe this endpoint once with this provider's key. A roster answer is
		// both "the key works here" and the list of ids that exist here — the
		// two facts the namespace alone cannot give.
		var roster map[string]bool
		if keyRef != "" && k.url != "" && !opt.NoProbe {
			if ids := probeModelIDs(ctx, k.url, ResolveKey(keyRef, opt), opt.ProbeTimeout); len(ids) > 0 {
				roster = make(map[string]bool, len(ids))
				for _, id := range ids {
					roster[id] = true
				}
			}
		}

		providerID := "custom"
		if id, ok := providerForBase(k.url); ok {
			providerID = id
		}

		for _, m := range rows {
			c := Candidate{
				Source:        SourceOpenCode,
				Provider:      providerID,
				Model:         m.ID,
				Label:         firstNonEmpty(m.Name, m.ID),
				BaseURL:       k.url,
				APIType:       "openai",
				KeyRef:        keyRef,
				ContextWindow: m.Limit.Context,
				Note:          k.provider,
			}
			switch {
			case keyRef == "":
				// The environment is the only credential this collector reads:
				// OpenCode keeps its own login in auth.json / opencode.db, and
				// that store belongs to the app (see the package comment). Naming
				// the variable is the whole fix — "no credential" alone reads as
				// a bug to anyone who has already run `opencode auth login`.
				switch {
				case freeOnProvider(k.provider, m.ID):
					// The free tier is a client-side grant, not a keyless API:
					// OpenCode's own CLI rides it and every outside call answers
					// 429 FreeUsageLimitError. Saying "export OPENCODE_API_KEY"
					// here would promise a free model the person cannot dial, so
					// the row says what is actually true and what would change it.
					c.Note = "opencode-free"
				case keyEnv != "":
					c.Note = k.provider + ": no " + keyEnv + " in this environment" +
						" (OpenCode's own login is not read — export it to import these)"
				default:
					c.Note = k.provider + ": no credential variable known for this provider"
				}
			case roster == nil:
				c.Note = k.provider + ": endpoint did not answer"
			case !roster[m.ID]:
				c.Note = k.provider + ": not in the endpoint roster"
			default:
				c.Importable = true
				// Say why a row is usable: the endpoint answered for it with
				// this provider's key. Without this the note is the bare
				// namespace, which looks like a label rather than a verdict.
				c.Note = k.provider + ": endpoint answered for this model"
				importable++
			}
			if !c.Importable {
				blocked++
			}
			src.Candidates = append(src.Candidates, c)
		}
	}

	switch {
	case len(src.Candidates) > 0:
		src.Status = StatusOK
		src.Detail = "opencode models · " + strconv.Itoa(len(src.Candidates)) + " model(s)"
		if importable > 0 {
			src.Detail += ", " + strconv.Itoa(importable) + " usable with this machine's keys"
		}
		if blocked > 0 && importable == 0 {
			src.Detail += ", none of them usable with this machine's keys"
			// A person who ran `opencode auth login` and still sees nothing
			// importable needs the missing fact, not a blank refusal: this
			// collector reads the environment only, by design.
			if one := singleEnvName(missingEnv); one != "" {
				src.Detail += " · OpenCode's own login (auth.json / opencode.db) is not read;" +
					" export " + one + " to make these importable"
			}
		}
	case len(models) == 0:
		src.Status = StatusEmpty
		src.Detail = "opencode models printed nothing"
	}
	return src
}

// freeOnProvider reports whether the catalogue serves this id on a free tier.
// Most free ids say so in the name ("deepseek-v4.1-flash-free"); big-pickle is
// the one the provider table has to name explicitly, which is why both
// spellings are checked.
func freeOnProvider(providerID, model string) bool {
	if strings.HasSuffix(strings.ToLower(strings.TrimSpace(model)), "-free") {
		return true
	}
	p, ok := providers.Lookup(providerID)
	if !ok {
		return false
	}
	spec, ok := p.SpecFor(model)
	return ok && spec.Free
}

// singleEnvName returns the one variable in the set, or "" when there are none
// or several — several means the fix is not a single export, so naming one
// would send the person after the wrong thing.
func singleEnvName(names map[string]bool) string {
	if len(names) != 1 {
		return ""
	}
	for name := range names {
		return name
	}
	return ""
}

// readOpenCodeCatalogue parses `models --verbose`: a `provider/model` header
// line followed by one pretty-printed JSON object per model. The objects are
// multi-line and nested, so the parser buffers until a top-level `}` closes the
// record rather than trying to read a line at a time.
func readOpenCodeCatalogue(ctx context.Context, cli string) ([]ocModel, error) {
	cmd := shimCommand(ctx, cli, "models", "--verbose")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// Wait runs beside the read, not after it: it is what closes the pipe when
	// the deadline kills the process, and the read is blocked on that pipe.
	waited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waited)
	}()

	var out []ocModel
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		var rec ocModel
		if json.Unmarshal([]byte(buf.String()), &rec) == nil && rec.ID != "" && rec.ProviderID != "" {
			out = append(out, rec)
		}
		buf.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A header line: `provider/model`, no quotes, not JSON.
		if buf.Len() == 0 && !strings.HasPrefix(trimmed, "{") && !strings.Contains(trimmed, `"`) && strings.Contains(trimmed, "/") {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		// Only a closing brace in column 0 ends the record. Nested objects close
		// with an indented "}" that trims to the same character, and treating
		// those as the end cut every record at its first nested field.
		if strings.TrimRight(line, " \t\r") == "}" {
			flush()
		}
	}
	flush()
	<-waited

	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, errString(detail)
		}
		if ctx.Err() != nil {
			return nil, errString("timed out waiting for the catalogue")
		}
	}
	return out, nil
}

// providerForBase maps an endpoint onto the catalogue provider that owns it.
// Matching the URL — not the namespace — is the point: `alibaba` and
// `alibaba-cn` are different services, and only one of them is the endpoint the
// qwen catalogue row dials.
func providerForBase(base string) (string, bool) {
	if strings.TrimSpace(base) == "" {
		return "", false
	}
	for _, p := range providers.All() {
		if sameURL(p.BaseURL, base) {
			return p.ID, true
		}
	}
	return "", false
}

// firstLine keeps a CLI's multi-line complaint to the one line a status chip
// can show.
func firstLine(s string) string {
	if line, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(line)
	}
	return s
}

// errString is a plain error for a subprocess's own words.
type errString string

func (e errString) Error() string { return string(e) }
