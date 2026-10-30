package apigateway

// IsLiteralTemplate reports whether a mapping template has none of '$',
// '#' and '\', the only characters Velocity interprets, so it renders
// unchanged (apigateway-rest.md 12.7). It is not implemented yet and
// returns false, so every template is refused.
func IsLiteralTemplate(string) bool { return false }

// MockStatus reads the status a rendered MOCK request template selects,
// accepting an unquoted statusCode key as AWS does (apigateway-rest.md
// 6.3). It is not implemented yet and reports none.
func MockStatus(string) (int, bool) { return 0, false }
