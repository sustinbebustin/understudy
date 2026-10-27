package apigatewayv2

import (
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

// childCollection is the kernel.WithResources resolver of a create or list
// call under an API: /apis/<api-id>/<sub> (apigateway-http.md 8.1).
func childCollection[In any](apiID func(*In) string, sub string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{resourceARN(c.Scope(), "/apis/"+apiID(in)+"/"+sub)}
	}
}
