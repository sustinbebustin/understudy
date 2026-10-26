package parity

// Mutate flips the recorded value at field, for the sensitivity check: a
// string or number changes its last digit, else its last letter, else gains
// an "x"; a bool flips; a container or null is removed; an absent field is
// added as "x". Not implemented yet: it refuses every field.
func Mutate(_ Response, field Path) (Response, error) {
	return Response{}, &PathError{Text: string(field), Reason: notImplemented("Mutate").Error()}
}
