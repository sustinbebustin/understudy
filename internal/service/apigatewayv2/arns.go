package apigatewayv2

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// resourceARN is an API Gateway resource ARN: arn:<partition>:apigateway:
// <region>::<path>, with an empty account field (apigateway-http.md 2.5).
func resourceARN(scope aws.Scope, path string) aws.ARN {
	return aws.ARN{Partition: scope.Partition.String(), Service: "apigateway", Region: scope.Region.String(), Resource: path}
}

// apiARN is the ARN of the API id in scope.
func apiARN(scope aws.Scope, id string) aws.ARN { return resourceARN(scope, "/apis/"+id) }

// apisCollection is the resource CreateApi and GetApis act on, /apis
// (apigateway-http.md 8.1).
func apisCollection[In any](c *kernel.Call, _ *In) []aws.ARN {
	return []aws.ARN{resourceARN(c.Scope(), "/apis")}
}

// apiItem is the kernel.WithResources resolver of a call on one API:
// /apis/<api-id> (apigateway-http.md 8.1).
func apiItem[In any](apiID func(*In) string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{apiARN(c.Scope(), apiID(in))}
	}
}

// childCollection is the kernel.WithResources resolver of a call on a
// resource under an API, /apis/<api-id>/<sub> (apigateway-http.md 8.1):
// a create or list call's collection, or the API's cors.
func childCollection[In any](apiID func(*In) string, sub string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{resourceARN(c.Scope(), "/apis/"+apiID(in)+"/"+sub)}
	}
}

// childItem is the kernel.WithResources resolver of a call on one resource
// under an API, /apis/<api-id>/<sub>/<id> (apigateway-http.md 8.1).
func childItem[In any](ids func(*In) (apiID, id string), sub string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		apiID, id := ids(in)
		return []aws.ARN{resourceARN(c.Scope(), "/apis/"+apiID+"/"+sub+"/"+id)}
	}
}

// resourceAt is the kernel.WithResources resolver of a call on the
// resource at path (apigateway-http.md 8.1), for the stage settings the
// collection and item forms do not reach.
func resourceAt[In any](path func(*In) string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{resourceARN(c.Scope(), path(in))}
	}
}

// tagsResource is the kernel.WithResources resolver of a tag operation:
// the tagged resource itself, as AWS's denials name it (APIGW2-15,
// recorded 2026-11-03: steps 13-18; apigateway-http.md 8.1), with the
// operation's primary action GET or PATCH (codegen/services). Whether AWS
// also asks about a /tags/... resource is unrecorded: the tagged
// resource's denial came first. A ResourceArn that is not an ARN, which
// the call then refuses, is named as /tags/<the text percent-encoded>.
func tagsResource[In any](resourceARN func(*In) string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		text := resourceARN(in)
		if arn, err := aws.ParseARN(text); err == nil {
			return []aws.ARN{arn}
		}
		return []aws.ARN{tagsARN(c.Scope(), text)}
	}
}

// tagsARN is /tags/<text percent-encoded>, every byte outside
// [A-Za-z0-9-_.~] written %XX: the tag resource form REST APIs document.
func tagsARN(scope aws.Scope, text string) aws.ARN {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(text) {
		switch ch := text[i]; {
		case 'A' <= ch && ch <= 'Z', 'a' <= ch && ch <= 'z', '0' <= ch && ch <= '9', strings.IndexByte("-_.~", ch) >= 0:
			b.WriteByte(ch)
		default:
			b.Write([]byte{'%', hex[ch>>4], hex[ch&0xF]})
		}
	}
	return resourceARN(scope, "/tags/"+b.String())
}
