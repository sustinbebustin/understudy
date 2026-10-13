package edge

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// HostPolicy is which Host headers a listener answers: the defence against
// DNS rebinding, in which a web page re-points its own name at the
// listener's address, so its requests become same-origin and their answers
// readable. Such requests still carry the page's name as Host.
//
//sumtype:decl
type HostPolicy interface{ isHostPolicy() }

// LocalHosts answers only Hosts no outside DNS server can point at the
// listener: none at all (HTTP/1.0), an IP literal, localhost and names
// under it, which browsers resolve to loopback themselves (RFC 6761 6.3),
// and the configured S3 base domains and names under them, which the
// operator chose. It suits a loopback listener and the container listener,
// whose clients address it by IP literal.
type LocalHosts struct{}

// NetworkHosts answers every Host from a client that is not a browser: an
// exposed listener's clients name it by whatever their network calls it,
// such as a compose service name. A browser's request (one that carries
// Origin or Sec-Fetch-Site) is held to LocalHosts, since a page that
// re-points its own name at a port published on loopback would otherwise
// read the answers.
type NetworkHosts struct{}

// AnyHost answers every Host, from browsers too; for a harness that sends
// arbitrary Hosts, never for a listener.
type AnyHost struct{}

func (LocalHosts) isHostPolicy()   {}
func (NetworkHosts) isHostPolicy() {}
func (AnyHost) isHostPolicy()      {}

// hostAllowed reports whether the listener answers r for its Host.
func (h *Handler) hostAllowed(r *http.Request) bool {
	switch h.opts.Hosts.(type) {
	case AnyHost:
		return true
	case NetworkHosts:
		if !fromBrowser(r) {
			return true
		}
	case LocalHosts, nil:
	}
	return localHost(r.Host, h.opts.S3BaseDomains)
}

// fromBrowser reports whether r carries a header only browsers set.
func fromBrowser(r *http.Request) bool {
	_, origin := r.Header["Origin"]
	return origin || r.Header.Get("Sec-Fetch-Site") != ""
}

// localHost reports whether host is a name no outside DNS server can point
// at the listener: none, an IP literal, localhost or a name under it, or
// one of the S3 base domains or a name under one.
func localHost(host string, s3BaseDomains []string) bool {
	name := strings.TrimSuffix(hostname(host), ".")
	if name == "" || isLocalhost(name) {
		return true
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	for _, base := range s3BaseDomains {
		if name == base || strings.HasSuffix(name, "."+base) {
			return true
		}
	}
	return false
}

// refuseHost is the answer to a Host the listener does not answer.
func refuseHost(host string) Response {
	return plainError(http.StatusForbidden, "understudy refused this request: its Host "+strconv.Quote(host)+
		" is not a name this listener answers. A loopback listener, and a browser on any listener, reach understudy only as localhost,"+
		" names under it, IP addresses and the configured S3 base domains, so a web page cannot reach it by re-pointing its own name"+
		" (DNS rebinding). Nothing was changed. Address understudy as localhost or 127.0.0.1, or as a name under localhost.")
}

// PagePolicy is which web pages may call the AWS APIs from a browser: the
// defence against a site the developer visits creating a queue with a form
// POST, or a function with a text/plain one, requests a browser sends to
// any address without a CORS preflight. The admin API answers no page on
// another site whatever the policy.
//
//sumtype:decl
type PagePolicy interface{ isPagePolicy() }

// LocalPages answers browser calls only from pages on a loopback origin:
// localhost, names under it and loopback addresses, where a developer's
// own app runs, and never from a page on the port the call was sent to or
// on any other port an understudy listener serves (ServedPorts). Such a
// page is one understudy served itself, an S3 object or a function URL
// response, whose content users or functions control; since the AWS APIs
// check no signature, it could otherwise read every secret and object with
// same-origin requests, or drive the AWS APIs from the TLS listener's port
// to the plain one. The page is the one the call's Origin names, else the
// one its Referer names, unless Sec-Fetch-Site is "none" (a navigation the
// user started); a same-origin or cross-site call that names no page is
// refused. The CLI and SDK clients send none of these headers.
type LocalPages struct{}

// AnyPage answers browser calls from pages on any site, for a developer
// whose app is served under a name that is not local.
type AnyPage struct{}

func (LocalPages) isPagePolicy() {}
func (AnyPage) isPagePolicy()    {}

// crossSitePage returns why an AWS API call is refused under the
// listener's PagePolicy as coming from a web page on another site.
func (h *Handler) crossSitePage(r *http.Request) (string, bool) {
	switch h.opts.Pages.(type) {
	case AnyPage:
		return "", false
	case LocalPages, nil:
	}
	if origin, sent := r.Header["Origin"]; sent {
		if len(origin) != 1 {
			return "it came from a web page on " + strconv.Quote(strings.Join(origin, ", ")) + ", which is not a single loopback origin", true
		}
		return pageRefused(r, h.served, origin[0], "it came from a web page on "+strconv.Quote(origin[0]))
	}
	// A browser sends no Fetch Metadata to a host it does not deem
	// potentially trustworthy, such as plain http on a configured S3 base
	// domain, nor does an older one anywhere, so a Referer names the page
	// even without Sec-Fetch-Site.
	site := strings.ToLower(r.Header.Get("Sec-Fetch-Site"))
	switch referer := r.Referer(); {
	case site == "none":
		return "", false
	case referer != "":
		return pageRefused(r, h.served, referer, "its Referer names the web page "+strconv.Quote(referer))
	case site == "" || site == "same-site":
		return "", false
	default:
		return "the browser marked it Sec-Fetch-Site: " + site + " and sent neither an Origin nor a Referer naming the page", true
	}
}

// pageRefused returns why a browser call from page, an origin or page URL,
// is refused: the page is not on a loopback origin, or it is on a port
// understudy serves, so understudy served it. what names the page.
func pageRefused(r *http.Request, served *ServedPorts, page, what string) (string, bool) {
	u, ok := loopbackPage(page)
	switch {
	case !ok:
		return what + ", which is not a loopback origin", true
	case servedByUnderstudy(r, served, u):
		return what + ", which is on a port understudy itself serves, so understudy served it (an S3 object or a function URL response) and its content is not trusted", true
	}
	return "", false
}

// servedByUnderstudy reports whether page, on a loopback origin, is on a
// port understudy serves: the one r's Host names, the one r's connection
// reached, or that of any listener in served. The Host's port counts since
// a published container port can differ from the port the listener binds;
// served counts since the TLS listener's pages reach the main listener
// cross-origin, and the reverse.
func servedByUnderstudy(r *http.Request, served *ServedPorts, page *url.URL) bool {
	port := page.Port()
	if port == "" {
		port = defaultPort(page.Scheme == "https")
	}
	if served.has(port) {
		return true
	}
	if _, hostPort, err := net.SplitHostPort(r.Host); err == nil {
		if hostPort == port {
			return true
		}
	} else if port == defaultPort(r.TLS != nil) {
		return true
	}
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if addr, err := netip.ParseAddrPort(local.String()); err == nil && strconv.Itoa(int(addr.Port())) == port {
			return true
		}
	}
	return false
}

// ServedPorts is the set of TCP ports understudy's listeners serve, which
// LocalPages refuses pages on. Handler.Server adds each listener's port as
// it starts serving. Handlers sharing one (Options.Served) refuse a page
// served on any of their listeners; the zero value is an empty set ready
// to use.
type ServedPorts struct {
	mu sync.RWMutex
	// ports holds decimal port numbers, as url.URL.Port gives them.
	ports map[string]struct{}
}

// add records the port of addr, a listener's address; an address with no
// port, which a TCP listener never has, adds nothing.
func (p *ServedPorts) add(addr net.Addr) {
	ap, err := netip.ParseAddrPort(addr.String())
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ports == nil {
		p.ports = map[string]struct{}{}
	}
	p.ports[strconv.Itoa(int(ap.Port()))] = struct{}{}
}

// has reports whether a listener serves port, a decimal port number.
func (p *ServedPorts) has(port string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.ports[port]
	return ok
}

// defaultPort is the port an http or https URL without one names.
func defaultPort(https bool) string {
	if https {
		return "443"
	}
	return "80"
}

// refusePage is the answer to an AWS API call crossSitePage refuses.
func refusePage(why string) Response {
	return plainError(http.StatusForbidden, "understudy refused this AWS API call: "+why+
		". understudy answers browser calls only from pages on localhost, names under it and loopback addresses,"+
		" and never from pages it serves itself, so neither a site you visit nor an uploaded file can create, invoke or read"+
		" your emulated resources. Nothing was changed. Serve your app from localhost or 127.0.0.1 on a port other than"+
		" understudy's, or make the call from an SDK outside the browser.")
}

// crossSiteAdmin returns why an admin call is refused as coming from a web
// page on another site, which could otherwise reset or read the emulator
// with a request that needs no CORS preflight: a cross-site Sec-Fetch-Site,
// or an Origin that is not loopback. The CLI and SDK clients send neither.
// It guards every path under /_understudy/ on every listener; the admin
// handler of the main listener then refuses more, any Origin including a
// loopback one, since no admin endpoint serves a page.
func crossSiteAdmin(r *http.Request) (string, bool) {
	if site := r.Header.Get("Sec-Fetch-Site"); strings.EqualFold(site, "cross-site") {
		return "the browser marked it Sec-Fetch-Site: cross-site", true
	}
	return foreignOrigin(r)
}

// foreignOrigin returns why r's Origin, when it carries one, is not a
// single loopback origin.
func foreignOrigin(r *http.Request) (string, bool) {
	if origin, sent := r.Header["Origin"]; sent && (len(origin) != 1 || !loopbackOrigin(origin[0])) {
		return "it came from a web page on " + strconv.Quote(strings.Join(origin, ", ")) + ", which is not a loopback origin", true
	}
	return "", false
}

// refuseCrossSite is the answer to an admin call crossSiteAdmin refuses.
func refuseCrossSite(why string) Response {
	return plainError(http.StatusForbidden, "understudy refused this admin call: "+why+
		". The admin API answers only clients without an Origin, such as the understudy CLI and the admin Go client,"+
		" so a site you visit cannot reset, reconfigure or read the emulator."+
		" Nothing was changed. Call it with the understudy CLI or from a tool that sends no Origin header.")
}

// loopbackOrigin reports whether origin, an origin or a page URL, is http
// or https on a loopback address, localhost or a name under it. "null" is
// not.
func loopbackOrigin(origin string) bool {
	_, ok := loopbackPage(origin)
	return ok
}

// loopbackPage parses origin, an origin or page URL, when loopbackOrigin
// holds for it.
func loopbackPage(origin string) (*url.URL, bool) {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, false
	}
	name := strings.ToLower(u.Hostname())
	if addr, err := netip.ParseAddr(name); err == nil {
		return u, addr.IsLoopback()
	}
	return u, isLocalhost(name)
}

// isLocalhost reports whether name is localhost or a name under it.
func isLocalhost(name string) bool {
	return name == "localhost" || strings.HasSuffix(name, ".localhost")
}
