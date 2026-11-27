package cmd

import (
	"time"

	"github.com/hemm-ems/hactl/internal/format"
)

// deadWindow is the --dead flag's value: a window, parsed and capped when
// the flag is set, so a bad value is refused before any request (D-238).
// Its type is "duration", not "string": the value has no case to fold, so it
// stands outside the filter case gate (D-2) by what it is.
type deadWindow struct {
	raw string
	d   time.Duration
}

func (w *deadWindow) String() string { return w.raw }

func (w *deadWindow) Set(v string) error {
	d, err := parseDeadWindow(v)
	if err != nil {
		return err
	}
	w.raw, w.d = v, d
	return nil
}

func (w *deadWindow) Type() string { return "duration" }

// set reports whether the caller asked for --dead.
func (w *deadWindow) set() bool { return w.d > 0 }

var (
	flagEntDead    deadWindow
	flagDeviceDead deadWindow
)

// addDeadColumns appends the verdict columns to a listing: `dead` is `yes`
// or `?` for a person and true / null for a machine (an undecided entity is
// not a false one), `dead_reason` names the rule that decided (D-238).
func addDeadColumns(tbl *format.Table, verdictOf func(i int) deadVerdict) {
	tbl.Headers = append(tbl.Headers, "dead", "dead_reason")
	for i := range tbl.Rows {
		v := verdictOf(i)
		cell, machine := "?", any(nil)
		if v.dead {
			cell, machine = "yes", true
		}
		tbl.Rows[i] = append(tbl.Rows[i], cell, v.reason)
		tbl.SetMachine(i, "dead", machine)
	}
}
