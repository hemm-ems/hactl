package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/hemm-ems/hactl/internal/companion"
)

// reloadReasonSuffix renders the companion's `reload_error` for a warning line,
// or "" when the reload succeeded or the companion predates the field.
//
// A bare "HA did not confirm reload" sends an operator hunting for a reason the
// companion already had — HA's own status and a bounded excerpt of its body, or
// the transport error class. `tpl create` used to paper over that by printing a
// rhetorical question ("is `template: !include template.yaml` in
// configuration.yaml?") at someone with no way to check the real answer. One
// helper rather than four inline formats: "present only on failure" is the kind
// of rule that gets re-derived correctly three times out of four.
func reloadReasonSuffix(reloadError string) string {
	if reloadError == "" {
		return ""
	}
	return ": " + reloadError
}

// checkHelperDomainWired fails a `helper create` preview exactly where the
// confirmed run would (H-2), by asking the companion whether Home Assistant
// reads the file a create for this domain would be written to.
//
// The verdict is the companion's, not a re-derivation: `reason` is the same
// string its POST answers 400 with, so the preview and the confirmed run
// explain an impossible create identically.
//
// An unreachable probe is an error, not a shrug. Skipping the check when the
// companion is too old to answer would restore precisely the behaviour this
// fixes — a confident plan for a create that cannot happen — and it would do so
// silently, which is worse than the original because the promise has by then
// been written down. The seam is version-pinned anyway (H-3: the vendored spec
// names the companion this CLI is contracted against), and the error names the
// route, so "your companion predates this" is legible from the message.
func checkHelperDomainWired(ctx context.Context, cc *companion.Client, domain string) error {
	verdict, err := cc.GetWiring(ctx, domain)
	if err != nil {
		return fmt.Errorf("checking whether HA reads %s config: %w", domain, err)
	}
	if verdict.Wired {
		return nil
	}
	return errors.New(verdict.Reason)
}
