package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// outputFormatFlagNames is the vocabulary of flags that decide what SHAPE a
// command's output takes. It is a set of NAMES rather than a list of sites,
// because the rule below has to hold for a flag nobody has added yet.
//
// `--json` is here even though it is global: it is the one every command has,
// so a command that adds a second format flag has created the conflict by
// doing so, and the day someone adds `--toml` beside it the gate over this set
// (TestOutputFormatFlagsAreExclusive) reports the new site rather than
// discovering it in a bug report.
//
// A flag named for the FORMAT of the output belongs here. A flag named for
// WHAT is in the output does not, however similar it reads: `config file --raw`
// means "leave !include directives unresolved", a decision about content, and
// it composes with `--json` perfectly well. That distinction is the manifest's
// only exemption and is recorded in dev/surfaces/outputformat.manifest.
var outputFormatFlagNames = []string{"json", "raw", "yaml"}

// checkOutputFormatFlags refuses an invocation that names more than one output
// format.
//
// `dash show pg-w6-dash --raw --yaml` printed compact JSON and said nothing
// about --yaml (finding #60); `--json --yaml` printed YAML. The precedence was
// the order of three if-statements, documented nowhere, and a caller who asked
// for YAML got JSON at exit 0 — the shape a machine consumer cannot detect.
//
// Naming the winner in the output is not available as a fix: under `--json`,
// a note on stdout breaks H-10's "it parses, with nothing else on stdout", and
// a note on stderr is the same silence one stream over. So the combination is
// refused, which is also the only answer that stays true when a third format
// arrives.
func checkOutputFormatFlags(cmd *cobra.Command) error {
	var chosen []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		for _, name := range outputFormatFlagNames {
			if f.Name == name {
				chosen = append(chosen, "--"+name)
			}
		}
	})
	if len(chosen) < 2 {
		return nil
	}
	sort.Strings(chosen)
	return fmt.Errorf("%s each name an output format and only one can be honoured; "+
		"pass exactly one (%s is the parseable document, %s is the same document indented, "+
		"%s is YAML)", strings.Join(chosen, " and "), "--raw", "--json", "--yaml")
}
