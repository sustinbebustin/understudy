package access

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// MintedPattern returns the policy Resource pattern Policy renders for a
// resource whose ARN carries a segment understudy draws rather than the
// caller names (a KMS key id, a Secrets Manager suffix, an SNS
// subscription id, an event source mapping UUID, the log stream of a
// function's execution environment), and true; for any other ARN it
// returns "" and false, and Policy renders the ARN exactly. mode is the
// recorded use's Mode: a log stream is minted only under AsExecutionRole.
//
// The pattern is the narrowest one matching every AWS-form id of the
// resource type, in the ARN's partition, region and account, so a policy
// rendered from one run allows the same calls in a run whose ids differ
// (ARCHITECTURE.md 17). The caller-named part (topic, secret or log group
// name) is escaped with policydoc.Literal; the wildcard the rule adds is
// not. Resource formats: the Service Authorization Reference resource-type
// tables for AWS KMS, Secrets Manager, Amazon SNS, AWS Lambda and Amazon
// CloudWatch Logs.
func MintedPattern(mode Mode, a aws.ARN) (pattern string, minted bool) {
	var named, wildcard string
	switch a.Service {
	case "kms":
		named, wildcard, minted = mintedKey(a.Resource)
	case "secretsmanager":
		named, wildcard, minted = mintedSecret(a.Resource)
	case "sns":
		named, wildcard, minted = mintedSubscription(a.Resource)
	case "lambda":
		named, wildcard, minted = mintedMapping(a.Resource)
	case "logs":
		if _, asRole := mode.(AsExecutionRole); asRole {
			named, wildcard, minted = mintedStream(a.Resource)
		}
	}
	if !minted {
		return "", false
	}
	scope := "arn:" + a.Partition + ":" + a.Service + ":" + a.Region + ":" + a.Account + ":"
	return policydoc.Literal(scope+named) + wildcard, true
}

// Each minted* rule splits an ARN's resource part into the part the
// pattern keeps exactly and the wildcard that replaces the minted id.

// mintedKey: key/${KeyId}.
func mintedKey(resource string) (named, wildcard string, ok bool) {
	id, ok := strings.CutPrefix(resource, "key/")
	return "key/", "*", ok && id != ""
}

// mintedSecret: secret:${SecretId}, the id being the name, a hyphen and the
// six ASCII letters or digits internal/service/secretsmanager/secretid.go
// draws.
func mintedSecret(resource string) (named, wildcard string, ok bool) {
	const suffix = len("-000000")
	id, ok := strings.CutPrefix(resource, "secret:")
	if !ok || len(id) <= suffix || id[len(id)-suffix] != '-' {
		return "", "", false
	}
	for _, c := range []byte(id[len(id)-suffix+1:]) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", "", false
		}
	}
	return "secret:" + id[:len(id)-suffix+1], "??????", true
}

// mintedSubscription: <topic>:<subscription id>, the subscription ARN being
// its topic ARN, a colon and the id.
func mintedSubscription(resource string) (named, wildcard string, ok bool) {
	topic, id, ok := strings.Cut(resource, ":")
	return topic + ":", "*", ok && topic != "" && id != "" && !strings.Contains(id, ":")
}

// mintedMapping: event-source-mapping:${UUID}.
func mintedMapping(resource string) (named, wildcard string, ok bool) {
	id, ok := strings.CutPrefix(resource, "event-source-mapping:")
	return "event-source-mapping:", "*", ok && id != ""
}

// mintedStream: log-group:${LogGroupName}:log-stream:${LogStreamName}, for
// the streams Lambda creates for an execution environment.
func mintedStream(resource string) (named, wildcard string, ok bool) {
	rest, ok := strings.CutPrefix(resource, "log-group:")
	if !ok {
		return "", "", false
	}
	group, stream, ok := strings.Cut(rest, ":log-stream:")
	return "log-group:" + group + ":log-stream:", "*", ok && group != "" && stream != ""
}
