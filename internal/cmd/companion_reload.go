package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// warnIfReformatted surfaces the companion's C-14 fallback on a write result.
//
// A single-entry write normally splices only that entry's lines. When the
// companion cannot — an anchor whose definition lived in the replaced entry, a
// layout its span arithmetic does not cover — it re-serializes the whole file
// and says so with `reformatted: true`. Formatting of entries the caller never
// touched may then have changed, which matters to anyone keeping config in git.
// Staying silent would let a whole-file rewrite read as the surgical write it
// was not, which is the defect the field exists to make visible.
func warnIfReformatted(res *writeResult, reformatted bool) *writeResult {
	if !reformatted {
		return res
	}
	return res.warn("the whole file was re-serialized, not just this entry — " +
		"formatting elsewhere in the file may have changed")
}

// warnTemplateEntities reports the entities a create declared but Home
// Assistant did not register (finding #91).
//
// A reload that answers 200 is not evidence an entity exists. HA validates each
// template entry during the reload, logs a schema error and skips that entry,
// and reports the reload itself as fine — so `select` given a YAML list where
// its `options` must be a template string, or `button` with no `press:`, was
// written, reported "created", and never became anything. The only account of
// it was in HA's log, which the caller had no reason to read because the
// command said the write worked.
//
// Each entity is named rather than collapsed into a count: a block declares
// several, and "2 of 3 entities were not created" leaves the caller to work out
// which two. `entities` also reaches --json untouched, so a machine reader gets
// the same per-entity answer this text does.
func warnTemplateEntities(res *writeResult, entities []companion.TemplateEntityResult) *writeResult {
	if len(entities) == 0 {
		return res
	}
	res = res.with("entities", entities)

	var missing []string
	for _, e := range entities {
		if !e.Created {
			missing = append(missing, fmt.Sprintf("%s.%s", e.Domain, e.UniqueID))
		}
	}
	if len(missing) == 0 {
		return res
	}
	return res.warn("written, but Home Assistant registered no entity for %s — "+
		"the entry is in the file and HA dropped it during reload, most often a per-domain schema it does "+
		"not satisfy (`select` needs `options` as a template string, `button` a `press:`, `number` a "+
		"`set_value:`). `hactl log --component config` carries HA's own reason",
		strings.Join(missing, ", "))
}
