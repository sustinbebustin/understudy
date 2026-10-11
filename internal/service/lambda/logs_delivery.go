package lambda

// Function output reaches CloudWatch Logs through the invoke path, which
// this build does not run; the configuration only names where it goes.

// logGroup is the function's log group: the configured one or
// /aws/lambda/<name>.
func logGroup(name string, l Logging) string {
	if l.Group != "" {
		return l.Group
	}
	return "/aws/lambda/" + name
}
