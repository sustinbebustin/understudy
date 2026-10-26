package sns

// Options are the SNS settings. The zero value is AWS's behavior.
type Options struct {
	// TLSPort returns the TLS listener's port once it serves, false while
	// there is none; nil means there never is one. HTTP deliveries base
	// their links on https://localhost:<port> when it reports one
	// (sns-http-delivery.md 8.3.3).
	TLSPort func() (uint16, bool)
}
