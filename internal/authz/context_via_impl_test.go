package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
)

// A forward-access call that names the service forwarding it carries
// kms:ViaService as <service>.<region>.amazonaws.com
// (iam-enforcement-surfaces.md 9.2), so an AWS managed key's policy, which
// admits the account's principals only through its service, can match; one
// that names none leaves it Unsupplied, as before.
func TestBuildContextSuppliesTheForwardingService(t *testing.T) {
	key := on("arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab", "111122223333")
	in := contextInput(userOf("111122223333", "ana", "AIDAANAEXAMPLE000001"), "kms:Decrypt", key)
	in.Via = authz.ForwardAccess{ViaService: "secretsmanager.us-east-1.amazonaws.com"}
	checkKeys(t, "forward access through Secrets Manager", authz.BuildContext(in), map[string]presence{
		"kms:ViaService":    present("secretsmanager.us-east-1.amazonaws.com"),
		"aws:ViaAWSService": present("true"),
	})
	in.Via = authz.ForwardAccess{}
	checkKeys(t, "forward access through an unnamed service", authz.BuildContext(in), map[string]presence{
		"kms:ViaService": unsupplied,
	})
}
