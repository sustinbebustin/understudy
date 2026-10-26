package server

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
)

// The router answers the evaluator's question, owner and stored policy,
// through the same namespace provider as the advisory's (contract 9.2): a
// bucket's owner and policy by an object's ARN, a role's trust policy, and
// none for a missing queue.
func TestWiredRouterReadsStoredPolicies(t *testing.T) {
	buildTable(t, func(k *kerneltest.K, _ *construction, built []wiredBuilt) {
		policies, err := policyProviders(built)
		if err != nil {
			k.Fatal(err)
		}
		var router authz.StoredPolicyReader = policies
		if _, err := k.Invoke(k.Context(), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "docs"}); err != nil {
			k.Fatal(err.AsError())
		}
		const trust = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
		role, rerr := k.Invoke(k.Context(), iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "worker", AssumeRolePolicyDocument: trust})
		if rerr != nil {
			k.Fatal(rerr.AsError())
		}
		parse := func(s string) aws.ARN {
			k.Helper()
			a, err := aws.ParseARN(s)
			if err != nil {
				k.Fatal(err)
			}
			return a
		}

		if got, ok := router.StoredPolicy(parse("arn:aws:s3:::docs/a.txt")); !ok || got != (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.NoPolicy{}}) {
			k.Errorf("StoredPolicy of an object through the router = %+v, %t; want NoPolicy owned by the bucket's account", got, ok)
		}
		got, ok := router.StoredPolicy(parse(role.Role.Arn))
		if p, parsed := got.Policy.(authz.Parsed); !ok || got.Owner != aws.DefaultAccount() || !parsed || len(p.Document.Statements) != 1 {
			k.Errorf("StoredPolicy of the role through the router = %+v, %t; want its trust policy, parsed", got, ok)
		}
		missing := aws.ARN{Partition: "aws", Service: "sqs", Region: "us-east-1", Account: "000000000000", Resource: "absent"}
		if got, ok := router.StoredPolicy(missing); ok {
			k.Errorf("StoredPolicy(%s) = %+v, want none", missing, got)
		}
	})
}

// adviceOnly reads resource policies only as the advisory does.
type adviceOnly struct {
	serveradmin.Service
	name ref.ServiceID
}

func (a adviceOnly) Name() ref.ServiceID { return a.name }

func (adviceOnly) ResourcePolicy(aws.ARN) (*policydoc.Document, bool) {
	return &policydoc.Document{}, true
}

// A service that answers only the advisory's question is not a provider,
// so no namespace answers one question and not the other.
func TestPolicySourcesSkipAServiceReadingPoliciesOneWay(t *testing.T) {
	built := []wiredBuilt{{&registry.Service{ID: "half", ARNNamespace: "h"}, adviceOnly{name: "half"}}}
	policies, err := policyProviders(built)
	if err != nil {
		t.Fatal(err)
	}
	target := aws.ARN{Partition: "aws", Service: "h", Resource: "r"}
	if doc, ok := policies.ResourcePolicy(target); ok || doc != nil {
		t.Errorf("ResourcePolicy = %v, %t; want none", doc, ok)
	}
	if got, ok := policies.StoredPolicy(target); ok {
		t.Errorf("StoredPolicy = %+v, want none", got)
	}
}
