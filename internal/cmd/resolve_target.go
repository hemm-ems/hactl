package cmd

import "strings"

// resolveRegistryTarget finds one registry entry by the identifier a caller
// supplied. The id wins over a name, always and across the whole list: HA
// allows a name that collides with another entry's id, and picking whichever
// came first in the list would make the same command delete different things
// depending on registry order.
//
// Names match case-insensitively because that is what `ent set-area` has
// always accepted, and a command must not refuse an identifier its siblings
// display and accept.
func resolveRegistryTarget[T any](want string, entries []T, key func(T) (id, name string)) (T, bool) {
	var zero T
	want = strings.TrimSpace(want)
	if want == "" {
		return zero, false
	}
	for _, e := range entries {
		if id, _ := key(e); id == want {
			return e, true
		}
	}
	for _, e := range entries {
		if _, name := key(e); strings.EqualFold(name, want) {
			return e, true
		}
	}
	return zero, false
}
