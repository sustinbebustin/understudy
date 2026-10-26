package parity

// notImplementedError is what the package's stubs return until the
// replayer is built.
type notImplementedError struct {
	op string
}

func (e *notImplementedError) Error() string {
	return "parity: " + e.op + " is not implemented yet"
}

func notImplemented(op string) error { return &notImplementedError{op: op} }
