package secretsmanager

import (
	"crypto/sha256"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// suffixLen is the length of the random part a secret ARN ends with,
// after a hyphen (secrets-logs-events-alarms.md 2.1).
const suffixLen = 6

// suffixAlphabet is the alphabet of ARN suffixes: AWS's documented examples
// (a1b2c3, nutBrk) use lower case, upper case and digits.
const suffixAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// secretRef is a SecretId resolved to the scope it names. An ARN names the
// account it carries; a full ARN matches only the secret that has it, so a
// recreated secret is not its predecessor, and a partial ARN (no suffix)
// matches by name.
type secretRef struct {
	scope aws.Scope
	name  string
	// arn is the full ARN when the SecretId was one; empty for a name.
	arn string
}

// parseSecretID reads a SecretId from c's point of view: a name, a full
// ARN or a partial ARN. ok is false for an ARN in another partition or
// Region, or one that is not a Secrets Manager secret's, which no secret
// in reach has.
func parseSecretID(c *kernel.Call, id string) (r secretRef, ok bool) {
	scope := c.Scope()
	if !strings.HasPrefix(id, "arn:") {
		return secretRef{scope: scope, name: id}, true
	}
	a, err := aws.ParseARN(id)
	if err != nil || a.Service != "secretsmanager" || a.Partition != scope.Partition.String() || a.Region != scope.Region.String() {
		return secretRef{}, false
	}
	rest, isSecret := strings.CutPrefix(a.Resource, "secret:")
	account, aerr := aws.ParseAccountID(a.Account)
	if !isSecret || rest == "" || aerr != nil {
		return secretRef{}, false
	}
	scope.Account = account
	return secretRef{scope: scope, name: rest, arn: id}, true
}

// find returns the secret r names in st: the one whose full ARN it is,
// else the one whose name it is.
func (r secretRef) find(st *State) (Secret, bool) {
	if r.arn != "" {
		if i := len(r.name) - suffixLen - 1; i > 0 && r.name[i] == '-' {
			if s, ok := st.Secrets[r.name[:i]]; ok && s.ARN == r.arn {
				return s, true
			}
		}
	}
	s, ok := st.Secrets[r.name]
	return s, ok
}

// secretARN is the ARN of a new secret: its name, a hyphen and a suffix
// derived from the request id of the call that creates it. Request ids
// come from the seeded identifier source, so a seeded run names secrets
// deterministically without a stream of its own, and every CreateSecret
// has its own request id, so a recreated secret gets another suffix.
func secretARN(scope aws.Scope, name string, request ids.RequestID) string {
	sum := sha256.Sum256([]byte(request))
	suffix := make([]byte, suffixLen)
	for i := range suffix {
		suffix[i] = suffixAlphabet[int(sum[i])%len(suffixAlphabet)]
	}
	return aws.ARN{
		Partition: scope.Partition.String(), Service: "secretsmanager", Region: scope.Region.String(),
		Account: scope.Account.String(), Resource: "secret:" + name + "-" + string(suffix),
	}.String()
}

// validName reports a name of the characters AWS documents for secret
// names: ASCII letters, digits and /_+=.@- (secrets-logs-events-alarms.md
// 2.1). Its length is the generated validator's.
func validName(name string) bool {
	for _, r := range name {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9':
		case strings.ContainsRune("/_+=.@-", r):
		default:
			return false
		}
	}
	return true
}
