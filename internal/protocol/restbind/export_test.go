package restbind

// The external tests exercise these helpers directly as well as through
// Decode and Encode.
var (
	SplitHeaderList = splitHeaderList
	JoinHeaderList  = joinHeaderList
	Labels          = pathLabels
)
