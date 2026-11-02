package access

import (
	"net/url"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// MintedPattern returns the policy Resource pattern Policy renders for a
// resource whose ARN carries a segment understudy draws rather than the
// caller names (a KMS key id, a Secrets Manager suffix, an SNS
// subscription id, an event source mapping UUID, the log stream of a
// function's execution environment, a DynamoDB stream label, an API
// Gateway API, route, integration, resource or deployment id), and true; for any
// other ARN it returns "" and false, and Policy renders the ARN exactly.
// mode is the recorded use's Mode: a log stream is minted only under
// AsExecutionRole.
//
// The pattern is the narrowest one matching every AWS-form id of the
// resource type, in the ARN's partition, region and account, so a policy
// rendered from one run allows the same calls in a run whose ids differ
// (ARCHITECTURE.md 17). The caller-named part (topic, secret, log group,
// table or stage name) is escaped with policydoc.Literal; the wildcards the
// rule adds are not. Resource formats: the Service Authorization Reference
// resource-type tables for AWS KMS, Secrets Manager, Amazon SNS, AWS
// Lambda, Amazon CloudWatch Logs and Amazon DynamoDB, and API Gateway's
// ARN reference (apigateway-http.md 8.1).
func MintedPattern(mode Mode, a aws.ARN) (pattern string, minted bool) {
	var rest []piece
	switch a.Service {
	case "kms":
		rest, minted = mintedKey(a.Resource)
	case "secretsmanager":
		rest, minted = mintedSecret(a.Resource)
	case "sns":
		rest, minted = mintedSubscription(a.Resource)
	case "lambda":
		rest, minted = mintedMapping(a.Resource)
	case "logs":
		if _, asRole := mode.(AsExecutionRole); asRole {
			rest, minted = mintedStream(a.Resource)
		}
	case "dynamodb":
		rest, minted = mintedStreamLabel(a.Resource)
	case "apigateway":
		rest, minted = mintedAPIGateway(a.Partition, a.Region, a.Resource)
	}
	if !minted {
		return "", false
	}
	scope := "arn:" + a.Partition + ":" + a.Service + ":" + a.Region + ":" + a.Account + ":"
	return render(append([]piece{named(scope)}, rest...), policydoc.Literal), true
}

// piece is one part of a minted pattern: text the caller named, kept
// exactly, or a wildcard the rule adds.
type piece struct {
	text string
	wild bool
}

func named(s string) piece    { return piece{text: s} }
func wildcard(s string) piece { return piece{text: s, wild: true} }

// render joins pieces, passing each run of adjacent named pieces through
// escape as one text (so policydoc.Literal sees a "${" split across two
// pieces) and each wildcard as it is.
func render(pieces []piece, escape func(string) string) string {
	var b, run strings.Builder
	for _, p := range pieces {
		if !p.wild {
			run.WriteString(p.text)
			continue
		}
		b.WriteString(escape(run.String()))
		run.Reset()
		b.WriteString(p.text)
	}
	b.WriteString(escape(run.String()))
	return b.String()
}

// Each minted* rule returns the pieces of an ARN's resource part: the
// parts the pattern keeps exactly and the wildcards that replace the
// minted ids.

// mintedKey: key/${KeyId}.
func mintedKey(resource string) ([]piece, bool) {
	id, ok := strings.CutPrefix(resource, "key/")
	return []piece{named("key/"), wildcard("*")}, ok && id != ""
}

// mintedSecret: secret:${SecretId}, the id being the name, a hyphen and the
// six ASCII letters or digits internal/service/secretsmanager/secretid.go
// draws.
func mintedSecret(resource string) ([]piece, bool) {
	const suffix = len("-000000")
	id, ok := strings.CutPrefix(resource, "secret:")
	if !ok || len(id) <= suffix || id[len(id)-suffix] != '-' {
		return nil, false
	}
	for _, c := range []byte(id[len(id)-suffix+1:]) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return nil, false
		}
	}
	return []piece{named("secret:" + id[:len(id)-suffix+1]), wildcard("??????")}, true
}

// mintedSubscription: <topic>:<subscription id>, the subscription ARN being
// its topic ARN, a colon and the id.
func mintedSubscription(resource string) ([]piece, bool) {
	topic, id, ok := strings.Cut(resource, ":")
	return []piece{named(topic + ":"), wildcard("*")}, ok && topic != "" && id != "" && !strings.Contains(id, ":")
}

// mintedMapping: event-source-mapping:${UUID}.
func mintedMapping(resource string) ([]piece, bool) {
	id, ok := strings.CutPrefix(resource, "event-source-mapping:")
	return []piece{named("event-source-mapping:"), wildcard("*")}, ok && id != ""
}

// mintedStream: log-group:${LogGroupName}:log-stream:${LogStreamName}, for
// the streams Lambda creates for an execution environment.
func mintedStream(resource string) ([]piece, bool) {
	rest, ok := strings.CutPrefix(resource, "log-group:")
	if !ok {
		return nil, false
	}
	group, stream, ok := strings.Cut(rest, ":log-stream:")
	return []piece{named("log-group:" + group + ":log-stream:"), wildcard("*")}, ok && group != "" && stream != ""
}

// mintedStreamLabel: table/${TableName}/stream/${StreamLabel}, the label
// being the instant DynamoDB enabled the stream, which the caller does not
// name.
func mintedStreamLabel(resource string) ([]piece, bool) {
	rest, ok := strings.CutPrefix(resource, "table/")
	if !ok {
		return nil, false
	}
	table, label, ok := strings.Cut(rest, "/stream/")
	return []piece{named("table/" + table + "/stream/"), wildcard("*")}, ok && table != "" && label != "" && !strings.Contains(table, "/")
}

// mintedAPIGateway: an API Gateway resource path, /apis/<api-id>[/...] of
// an HTTP API (apigateway-http.md 8.1) or /restapis/<api-id>[/...] of a
// REST API (apigateway-rest.md 3.10), or the tags resource of one,
// /tags/<its ARN percent-encoded>.
func mintedAPIGateway(partition, region, resource string) ([]piece, bool) {
	if encoded, ok := strings.CutPrefix(resource, "/tags/"); ok {
		return mintedTags(partition, region, encoded)
	}
	return mintedAPIPath(resource)
}

// mintedAPIPath renders an API id, ten lower-case letters or digits, as
// ??????????, as AWS's own policy example does ("requires that resources
// have a maximum of 10 characters", apigateway-http.md 8.1), and a minted
// child id under it as *, since their AWS lengths are UNVERIFIED
// (APIGW2-03, APIGW1-02; a REST API's root resource id is 10 characters,
// its other resource ids 6). Stage names, methods, status codes and the
// other path parts are the caller's. The /apis and /restapis collections
// name no id and are not minted.
//
// The child collections whose next segment understudy mints (internal/ids
// apigateway.go) are routes, integrations and deployments of an HTTP API,
// and resources and deployments of a REST API.
func mintedAPIPath(path string) ([]piece, bool) {
	if rest, ok := strings.CutPrefix(path, "/apis/"); ok {
		return mintedAPIUnder("/apis/", []string{"routes", "integrations", "deployments"}, rest)
	}
	if rest, ok := strings.CutPrefix(path, "/restapis/"); ok {
		return mintedAPIUnder("/restapis/", []string{"resources", "deployments"}, rest)
	}
	return nil, false
}

// mintedAPIUnder renders rest, the part of an API path after collection,
// children being the collection's child collections of minted ids.
func mintedAPIUnder(collection string, children []string, rest string) ([]piece, bool) {
	id, tail, more := strings.Cut(rest, "/")
	if !apiGatewayID(id, 10) {
		return nil, false
	}
	out := []piece{named(collection), wildcard("??????????")}
	if !more {
		return out, true
	}
	sub, child, hasChild := strings.Cut(tail, "/")
	out = append(out, named("/"+sub))
	if !hasChild {
		return out, true
	}
	childID, after, deeper := strings.Cut(child, "/")
	if slices.Contains(children, sub) && apiGatewayID(childID, 0) {
		out = append(out, named("/"), wildcard("*"))
		if deeper {
			out = append(out, named("/"+after))
		}
		return out, true
	}
	return append(out, named("/"+child)), true
}

// apiGatewayID reports whether s is an id of the API Gateway alphabet
// (internal/ids apigateway.go): lower-case letters and digits, of length n,
// or of any non-zero length when n is 0.
func apiGatewayID(s string, n int) bool {
	if s == "" || (n > 0 && len(s) != n) {
		return false
	}
	for _, c := range []byte(s) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// mintedTags renders /tags/<encoded ARN> whose ARN is a minted API Gateway
// resource of the same partition and region: its named parts encoded as
// the tag operations encode them, its wildcards as they are.
func mintedTags(partition, region, encoded string) ([]piece, bool) {
	text, err := url.PathUnescape(encoded)
	if err != nil {
		return nil, false
	}
	tagged, err := aws.ParseARN(text)
	if err != nil || tagged.Service != "apigateway" || tagged.Partition != partition || tagged.Region != region || tagged.Account != "" {
		return nil, false
	}
	inner, ok := mintedAPIPath(tagged.Resource)
	if !ok {
		return nil, false
	}
	scope := "arn:" + tagged.Partition + ":apigateway:" + tagged.Region + "::"
	return []piece{named("/tags/"), wildcard(render(append([]piece{named(scope)}, inner...), percentEncode))}, true
}

// percentEncode writes every byte outside [A-Za-z0-9-_.~] as %XX, as
// internal/service/apigatewayv2/arns.go tagsARN and
// internal/service/apigateway/tags.go tagsResource encode a tagged ARN, and
// escapes the result as policy text.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		switch ch := s[i]; {
		case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9', strings.IndexByte("-_.~", ch) >= 0:
			b.WriteByte(ch)
		default:
			b.Write([]byte{'%', hex[ch>>4], hex[ch&0xF]})
		}
	}
	return b.String()
}
