package smithy

// Deref is *p, or the zero value for nil. Generated API structs carry
// optional members as pointers, so service code reads them through it.
func Deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
