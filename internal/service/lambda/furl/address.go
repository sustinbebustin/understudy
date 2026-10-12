package furl

import (
	"net"
	"strings"
)

// PathPrefix is the path fallback's prefix, which ARCHITECTURE.md 4.1
// rule 1 reserves on the main listener: /_understudy/lambda-url/<url-id>/<path>
// names the function URL <url-id> with <path>, for clients that cannot
// resolve *.localhost.
const PathPrefix = "/_understudy/lambda-url/"

// Address is the function URL a request names and the path the function
// sees.
type Address struct {
	// ID is the url id, lowercase: host names are case-insensitive.
	ID string
	// Host is the function URL host the request named, lowercase and
	// without its port: <url-id>.lambda-url.<region>.<base>. It is empty
	// for the path fallback, which names no host.
	Host string
	// Region is the region Host names; empty for the path fallback.
	Region string
	// RawPath is the escaped path as the client sent it, without the path
	// fallback's prefix; "/" at least.
	RawPath string
}

// Resolve reads the function URL a request names from its Host header and
// its escaped path: the path fallback when the path starts with
// PathPrefix, otherwise the host form <url-id>.lambda-url.<region>.<base>.
// false when it names neither.
func Resolve(host, rawPath string) (Address, bool) {
	if rest, ok := strings.CutPrefix(rawPath, PathPrefix); ok {
		id, tail, _ := strings.Cut(rest, "/")
		if !validID(id) {
			return Address{}, false
		}
		return Address{ID: strings.ToLower(id), RawPath: "/" + tail}, true
	}
	name := hostname(host)
	labels := strings.SplitN(name, ".", 4)
	if len(labels) != 4 || labels[1] != "lambda-url" || !validID(labels[0]) || labels[2] == "" || labels[3] == "" {
		return Address{}, false
	}
	if rawPath == "" {
		rawPath = "/"
	}
	return Address{ID: labels[0], Host: name, Region: labels[2], RawPath: rawPath}, true
}

// Domain is the domain name the request event reports: the host the
// request named, or for the path fallback the host form of the URL in
// region, under localhost as Lambda reports FunctionUrl.
func (a Address) Domain(region string) string {
	if a.Host != "" {
		return a.Host
	}
	return a.ID + ".lambda-url." + region + ".localhost"
}

// validID reports a url id's form: letters and digits only.
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// hostname is a Host header without its port, lowercase.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}
