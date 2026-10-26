package admin

// UISessionHeader carries the web UI's session secret on admin calls (ADR
// 0020): a call a web page makes is answered only when it is same-origin
// and carries this run's secret.
const UISessionHeader = "Understudy-UI-Session"

// UISession is GET /ui/session.
type UISession struct {
	// Valid reports whether the call carried this run's UI session secret
	// in UISessionHeader.
	Valid bool `json:"valid"`
}
