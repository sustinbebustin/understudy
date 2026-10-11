package lambda

import (
	"net"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// functionURL is the URL a function URL configuration answers on:
// http://<url-id>.lambda-url.<region>.localhost:<port>/, on the port the
// client addressed (ARCHITECTURE.md 14.7). The edge routes the host form
// and the /_understudy/lambda-url/<url-id>/ fallback to InvokeFunctionUrl,
// which this build does not bind yet.
func functionURL(e kernel.Endpoint, scope aws.Scope, id string) string {
	host := id + ".lambda-url." + scope.Region.String() + ".localhost"
	if _, port, err := net.SplitHostPort(e.Host); err == nil && port != "" {
		host = net.JoinHostPort(host, port)
	}
	return "http://" + host + "/"
}
