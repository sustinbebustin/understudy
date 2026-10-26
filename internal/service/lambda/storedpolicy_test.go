package lambda_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/lambda"
)

// The evaluator reads a function's permissions through StoredPolicyReader:
// NoPolicy until AddPermission, then the parsed statements, owned by the
// function's account; a function that does not exist names none, as
// ResourcePolicy answers (iam-enforcement-surfaces.md 7.4).
func TestStoredPolicyReadsTheFunctionPermissions(t *testing.T) {
	run(t, lambda.Options{}, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		fn := h.createFunction("guarded")
		arn, err := aws.ParseARN(*fn.FunctionArn)
		if err != nil {
			h.Fatal(err)
		}
		if got, ok := reader.StoredPolicy(arn); !ok || got != (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.NoPolicy{}}) {
			h.Errorf("StoredPolicy without permissions = %+v, %t; want NoPolicy owned by the function's account", got, ok)
		}
		call(h, lambdaapi.OpAddPermission, &lambdaapi.AddPermissionInput{
			FunctionName: "guarded", StatementId: "sns", Action: "lambda:InvokeFunction", Principal: "sns.amazonaws.com",
		})
		got, ok := reader.StoredPolicy(arn)
		p, parsed := got.Policy.(authz.Parsed)
		if !ok || got.Owner != aws.DefaultAccount() || !parsed || len(p.Document.Statements) != 1 || p.Document.Statements[0].Sid != "sns" {
			h.Errorf("StoredPolicy after AddPermission = %+v, %t; want the one statement, parsed", got, ok)
		}
		gone := arn
		gone.Resource = "function:missing"
		if got, ok := reader.StoredPolicy(gone); ok {
			h.Errorf("StoredPolicy of a missing function = %+v, want none", got)
		}
	})
}
