package lambda_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// ResourcePolicy reads a function's permissions for the delivery advisory:
// none until AddPermission, then the statements GetPolicy returns; a
// function that does not exist has none to read.
func TestResourcePolicyReadsTheFunctionPermissions(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		fn := h.createFunction("advised")
		arn, err := aws.ParseARN(*fn.FunctionArn)
		if err != nil {
			h.Fatal(err)
		}
		if doc, ok := h.svc.ResourcePolicy(arn); !ok || doc != nil {
			h.Fatalf("ResourcePolicy without permissions = %+v, %t; want no policy for an existing function", doc, ok)
		}
		call(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "advised", StatementId: "sns", Action: "lambda:InvokeFunction", Principal: "sns.amazonaws.com",
		})
		doc, ok := h.svc.ResourcePolicy(arn)
		if !ok || doc == nil || len(doc.Statements) != 1 || doc.Statements[0].Sid != "sns" {
			h.Errorf("ResourcePolicy after AddPermission = %+v, %t; want the one statement", doc, ok)
		}
		gone := arn
		gone.Resource = "function:missing"
		if doc, ok := h.svc.ResourcePolicy(gone); ok || doc != nil {
			h.Errorf("ResourcePolicy of a missing function = %+v, %t; want none", doc, ok)
		}
	})
}
