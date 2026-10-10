package aws_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
)

func TestValidIAMPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/":                                  true,
		"/service-role/":                     true,
		"/division_abc/subdivision_xyz/":     true,
		"/" + strings.Repeat("a", 510) + "/": true,
		"/" + strings.Repeat("a", 511) + "/": false,
		"":                                   false,
		"//":                                 false,
		"/a b/":                              false,
		"/é/":                                false,
		"nopath":                             false,
		"/service-role":                      false,
		"service-role/":                      false,
	} {
		if got := aws.ValidIAMPath(path); got != want {
			t.Errorf("ValidIAMPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestValidIAMName(t *testing.T) {
	for name, want := range map[string]bool{
		"r":                     true,
		"Alice":                 true,
		"svc+=,.@-_9":           true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		"":                      false,
		"a/b":                   false,
		"a b":                   false,
		"é":                     false,
	} {
		if got := aws.ValidIAMName(name); got != want {
			t.Errorf("ValidIAMName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestValidRoleSessionName(t *testing.T) {
	for name, want := range map[string]bool{
		"understudy":            true,
		"ab":                    true,
		strings.Repeat("a", 64): true,
		strings.Repeat("a", 65): false,
		"a":                     false,
		"":                      false,
		"a/b":                   false,
	} {
		if got := aws.ValidRoleSessionName(name); got != want {
			t.Errorf("ValidRoleSessionName(%q) = %v, want %v", name, got, want)
		}
	}
}
