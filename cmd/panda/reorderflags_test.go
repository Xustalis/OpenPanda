// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

// TestReorderFlagsKeepsValuesAttached pins the argv contract every command
// depends on: reorderFlags reads which flags carry a value off the FlagSet
// itself, so a flag added tomorrow cannot repeat the incidents the hand-kept
// tables produced — `--secret` consuming `--peer` in the documented
// `panda pair --secret s --peer a` join line, `--priority` reading the plan
// path in `plan run plan.yaml --priority high`, and `task artifact <id>
// --file f` taking the id as the filename.
func TestReorderFlagsKeepsValuesAttached(t *testing.T) {
	pairFS := func(fs *flag.FlagSet) {
		fs.String("config", "", "")
		fs.String("card", "", "")
		fs.String("secret", "", "")
		fs.String("peer", "", "")
		fs.String("listen", "", "")
		fs.String("trust", "", "")
		fs.Bool("yes", false, "")
	}
	artifactFS := func(fs *flag.FlagSet) {
		fs.String("config", "", "")
		fs.String("out", "", "")
		fs.String("file", "", "")
		fs.Bool("list", false, "")
	}
	planFS := func(fs *flag.FlagSet) {
		fs.String("config", "", "")
		fs.String("card", "", "")
		fs.String("priority", "", "")
		fs.Bool("dry-run", false, "")
	}
	aliasFS := func(fs *flag.FlagSet) {
		m := fs.String("m", "", "")
		fs.StringVar(m, "note", "", "")
	}

	for _, tc := range []struct {
		name          string
		defs          func(*flag.FlagSet)
		args          []string
		want          []string
		wantParseFail bool
	}{
		{
			name: "the legacy pair line keeps its own values",
			defs: pairFS,
			args: []string{"--secret", "s3cret", "--peer", "10.0.0.9:7836"},
			want: []string{"--secret", "s3cret", "--peer", "10.0.0.9:7836"},
		},
		{
			name: "task artifact --file after the id",
			defs: artifactFS,
			args: []string{"01a", "--file", "out.txt"},
			want: []string{"--file", "out.txt", "01a"},
		},
		{
			name: "task artifact --out after the id",
			defs: artifactFS,
			args: []string{"01a", "--out", "dir"},
			want: []string{"--out", "dir", "01a"},
		},
		{
			name: "task artifact --file=out.txt stays one token",
			defs: artifactFS,
			args: []string{"01a", "--file=out.txt"},
			want: []string{"--file=out.txt", "01a"},
		},
		{
			name: "plan run --priority after the path",
			defs: planFS,
			args: []string{"plan.yaml", "--priority", "high"},
			want: []string{"--priority", "high", "plan.yaml"},
		},
		{
			// A bool must hoist BARE: were it read as value-carrying,
			// "other" would be attached to --dry-run and jump ahead of "pos".
			name: "bool flags do not swallow the next word",
			defs: planFS,
			args: []string{"pos", "--dry-run", "other"},
			want: []string{"--dry-run", "pos", "other"},
		},
		{
			// A second definition (flag.StringVar) is value-carrying too:
			// VisitAll lists both spellings of the same variable.
			name: "alias definitions carry values as well",
			defs: aliasFS,
			args: []string{"id", "--note", "x"},
			want: []string{"--note", "x", "id"},
		},
		{
			// A flag absent from the FlagSet keeps its old behaviour:
			// hoisted bare, value left behind — flag.Parse then reports it
			// as an error instead of misreading it as a value.
			name:          "undefined flag still hoists bare",
			defs:          artifactFS,
			args:          []string{"id", "--nope", "val"},
			want:          []string{"--nope", "id", "val"},
			wantParseFail: true,
		},
		{
			// "--" ends flag scanning: what follows is literal even when it
			// looks like a flag — `task send <id> -- --weird` sends the text.
			// The terminator stays in the output so flag.Parse stops at it.
			name: "double dash terminates flag scanning",
			defs: artifactFS,
			args: []string{"--out", "d", "--", "--not-a-flag"},
			want: []string{"--out", "d", "--", "--not-a-flag"},
		},
		{
			// Positionals keep their written order across the terminator:
			// "a" was typed before "--", "-x" after — hoisting -x ahead of a
			// would silently reorder the user's words.
			name: "positional order survives across double dash",
			defs: artifactFS,
			args: []string{"a", "--", "-x"},
			want: []string{"--", "a", "-x"},
		},
		{
			// A bare "-" is a positional (stdin convention), not a flag: had
			// it hoisted into the flag run, flag.Parse would stop at it and
			// strand the real flags behind it.
			name: "lone dash is a positional",
			defs: artifactFS,
			args: []string{"--out", "d", "-", "--list"},
			want: []string{"--out", "d", "--list", "-"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet(tc.name, flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			tc.defs(fs)
			got := reorderFlags(fs, tc.args)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("reorderFlags(%v) = %v, want %v", tc.args, got, tc.want)
			}
			// The reordered argv must leave the FlagSet's own parsing with
			// every flag accepted — the values survive end to end.
			err := fs.Parse(got)
			if tc.wantParseFail {
				if err == nil {
					t.Fatalf("flag.Parse(%v): undefined flag went unreported", got)
				}
			} else if err != nil {
				t.Fatalf("flag.Parse(%v): %v", got, err)
			}
		})
	}
}
