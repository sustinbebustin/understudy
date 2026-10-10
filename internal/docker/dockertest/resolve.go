package dockertest

import "strings"

// resolve finds the object ref names in byID, keyed by full ID, in the order
// the Docker daemon resolves container and network references: the full ID,
// then an exact name, then an ID prefix. A name or prefix shared by several
// objects is ambiguous and resolves to nothing, so the result never depends
// on map iteration order. Callers hold d.mu.
func resolve[T any](byID map[string]T, ref string, name func(T) string) (T, bool) {
	var zero T
	if ref == "" {
		return zero, false
	}
	if o, ok := byID[ref]; ok {
		return o, true
	}
	if o, ok := unique(byID, func(_ string, o T) bool { return name(o) == ref }); ok {
		return o, true
	}
	return unique(byID, func(id string, _ T) bool { return strings.HasPrefix(id, ref) })
}

// unique returns the one object match accepts, or false when none or several
// do.
func unique[T any](byID map[string]T, match func(id string, o T) bool) (T, bool) {
	var found T
	n := 0
	for id, o := range byID {
		if match(id, o) {
			found = o
			n++
		}
	}
	if n != 1 {
		var zero T
		return zero, false
	}
	return found, true
}
