package server

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// The router reaches the bucket policy, by the bucket's ARN and an
// object's, and a secret's resource policy through their namespaces'
// providers, beside the key and trust policies
// TestWiredPolicyProvidersCoverEveryResourcePolicyKind reads.
func TestWiredRouterReadsBucketAndSecretPolicies(t *testing.T) {
	buildTable(t, func(k *kerneltest.K, _ *construction, built []wiredBuilt) {
		policies, err := policyProviders(built)
		if err != nil {
			k.Fatal(err)
		}
		var router authz.PolicyProvider = policies
		const bucketPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/reader"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::docs/*"}]}`
		const secretPolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:role/app"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
		if _, err := k.Invoke(k.Context(), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "docs"}); err != nil {
			k.Fatal(err.AsError())
		}
		if _, err := k.Invoke(k.Context(), s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "docs", Policy: bucketPolicy}); err != nil {
			k.Fatal(err.AsError())
		}
		secret, serr := k.Invoke(k.Context(), secretsmanagerapi.OpCreateSecret, &secretsmanagerapi.CreateSecretInput{
			Name: "app/db", ClientRequestToken: new("terraform-00000000000000000000000001"), SecretString: new("v"),
		})
		if serr != nil {
			k.Fatal(serr.AsError())
		}
		if secret.ARN == nil {
			k.Fatal("CreateSecret returned no ARN")
		}
		if _, err := k.Invoke(k.Context(), secretsmanagerapi.OpPutResourcePolicy, &secretsmanagerapi.PutResourcePolicyInput{SecretId: *secret.ARN, ResourcePolicy: secretPolicy}); err != nil {
			k.Fatal(err.AsError())
		}

		for _, s := range []string{"arn:aws:s3:::docs", "arn:aws:s3:::docs/a.txt", *secret.ARN} {
			a, err := aws.ParseARN(s)
			if err != nil {
				k.Fatal(err)
			}
			if doc, ok := router.ResourcePolicy(a); !ok || doc == nil {
				k.Errorf("ResourcePolicy(%s) through the router = %+v, %t; want its policy", s, doc, ok)
			}
		}
	})
}
