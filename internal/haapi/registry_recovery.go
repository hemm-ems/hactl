package haapi

import "fmt"

// withRegistryRecovery adds a way out to a registry listing that decoded
// without an identity.
//
// H-14 fails the whole listing when one record arrives without its identity,
// and that is the right call: a listing quietly missing a row is the disease
// the poison exists to prevent. But `area delete` has to list the registry
// before it can resolve anything, so one area with a blank `area_id` takes
// down `ls`, `create` and `delete` together — hactl cannot clean up after
// hactl, and the diagnosis alone left the operator in the live-fire report to
// work out the raw-WebSocket escape on their own while every `area` command in
// the session kept failing.
//
// Creating such a record is now refused client-side (cmd.requireRegistryName),
// so this path is for the ones already out there — and for the other cause the
// same message covers, a wire field hactl no longer decodes, which the wrapped
// text still names first.
//
// The wrap keeps degeneracy.Marker (the integration harness greps command
// errors for it) and, through %w, degeneracy.ErrDegenerate, which callers use
// to tell "this source is unavailable" from "this source changed shape".
func withRegistryRecovery(err error, kind string) error {
	return fmt.Errorf("%w — if one %[2]s really carries a blank %[2]s_id (Home Assistant accepts a "+
		"blank name and mints one), it fails every 'hactl %[2]s' command until it is gone, and "+
		"'%[2]s delete' cannot remove it because it must list the registry first: delete it in Home "+
		"Assistant's UI, or over a raw WebSocket with config/%[2]s_registry/delete and an empty "+
		"%[2]s_id", err, kind)
}
