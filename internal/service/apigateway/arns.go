package apigateway

import (
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// resourceARN is an API Gateway resource ARN: arn:<partition>:apigateway:
// <region>::<path>, with an empty account field (apigateway-rest.md 3.10).
func resourceARN(scope aws.Scope, path string) aws.ARN {
	return aws.ARN{Partition: scope.Partition.String(), Service: "apigateway", Region: scope.Region.String(), Resource: path}
}

// restAPIARN is the ARN of the REST API id in scope.
func restAPIARN(scope aws.Scope, id string) aws.ARN { return resourceARN(scope, "/restapis/"+id) }

// resourceAt is the kernel.WithResources resolver of a call on the
// resource at path, from the REST 3.10 table: /restapis for the
// collection, /restapis/<id>/... for an item or a child collection.
func resourceAt[In any](path func(*In) string) func(*kernel.Call, *In) []aws.ARN {
	return func(c *kernel.Call, in *In) []aws.ARN {
		return []aws.ARN{resourceARN(c.Scope(), path(in))}
	}
}
