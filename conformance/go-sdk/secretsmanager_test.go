package gosdk_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

// Research: docs/research/phase1/secrets-logs-events-alarms.md section 2,
// error-matrix.md 5.8.

// createSecret creates a secret holding value and force-deletes it when
// the test ends.
func createSecret(t *testing.T, secretName, value string) *secretsmanager.CreateSecretOutput {
	t.Helper()
	client := secretsmanager.NewFromConfig(awsCfg)
	out, err := client.CreateSecret(t.Context(), &secretsmanager.CreateSecretInput{Name: aws.String(secretName), SecretString: aws.String(value)})
	ok(t, err, "CreateSecret "+secretName)
	t.Cleanup(func() {
		_, _ = client.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: out.ARN, ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	return out
}

// stagesOf returns the staging labels DescribeSecret reports for version.
func stagesOf(t *testing.T, client *secretsmanager.Client, secretID, version string) []string {
	t.Helper()
	out, err := client.DescribeSecret(t.Context(), &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretID)})
	ok(t, err, "DescribeSecret")
	stages := slices.Clone(out.VersionIdsToStages[version])
	slices.Sort(stages)
	return stages
}

func TestSecretsManagerLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := secretsmanager.NewFromConfig(awsCfg)
	acct := account(t)
	secretName := name("app/db")

	created, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
		Name: aws.String(secretName), SecretString: aws.String(`{"user":"app"}`), Description: aws.String("conformance"),
		Tags: []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreateSecret")
	t.Cleanup(func() {
		_, _ = client.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: created.ARN, ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	arn := aws.ToString(created.ARN)
	v1 := aws.ToString(created.VersionId)

	t.Run("CreateSecret response", func(t *testing.T) {
		re := regexp.MustCompile(`^arn:aws:secretsmanager:` + regexp.QuoteMeta(region()) + `:` + acct + `:secret:` + regexp.QuoteMeta(secretName) + `-[A-Za-z0-9]{6}$`)
		if !re.MatchString(arn) {
			t.Errorf("ARN = %q, want %s", arn, re)
		}
		if aws.ToString(created.Name) != secretName || v1 == "" {
			t.Errorf("CreateSecret = Name %q, VersionId %q", aws.ToString(created.Name), v1)
		}
	})

	t.Run("GetSecretValue", func(t *testing.T) {
		for _, id := range []string{secretName, arn} {
			out, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(id)})
			ok(t, err, "GetSecretValue by "+id)
			if aws.ToString(out.SecretString) != `{"user":"app"}` || aws.ToString(out.VersionId) != v1 || !slices.Equal(out.VersionStages, []string{"AWSCURRENT"}) ||
				aws.ToString(out.ARN) != arn || out.SecretBinary != nil || out.CreatedDate == nil {
				t.Errorf("GetSecretValue(%s) = SecretString %q, VersionId %q, stages %v, ARN %q, binary %v", id, aws.ToString(out.SecretString), aws.ToString(out.VersionId), out.VersionStages, aws.ToString(out.ARN), out.SecretBinary)
			}
		}
	})

	t.Run("DescribeSecret", func(t *testing.T) {
		out, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
		ok(t, err, "DescribeSecret")
		if aws.ToString(out.ARN) != arn || aws.ToString(out.Description) != "conformance" || out.CreatedDate == nil || out.LastChangedDate == nil {
			t.Errorf("DescribeSecret = ARN %q, Description %q, CreatedDate %v, LastChangedDate %v", aws.ToString(out.ARN), aws.ToString(out.Description), out.CreatedDate, out.LastChangedDate)
		}
		if !slices.Equal(out.VersionIdsToStages[v1], []string{"AWSCURRENT"}) {
			t.Errorf("VersionIdsToStages = %v, want %s: [AWSCURRENT]", out.VersionIdsToStages, v1)
		}
		if len(out.Tags) != 1 || aws.ToString(out.Tags[0].Key) != "team" {
			t.Errorf("Tags = %+v, want team=platform", out.Tags)
		}
		if out.KmsKeyId != nil || out.DeletedDate != nil {
			t.Errorf("DescribeSecret KmsKeyId %q, DeletedDate %v; want both absent (default key, not deleted)", aws.ToString(out.KmsKeyId), out.DeletedDate)
		}
		if out.LastAccessedDate == nil {
			t.Errorf("DescribeSecret LastAccessedDate is absent after a GetSecretValue")
		}
	})

	t.Run("errors", func(t *testing.T) {
		_, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String(secretName), SecretString: aws.String("x")})
		errAs[*types.ResourceExistsException](t, err, "CreateSecret with an existing name")
		wantMessage(t, err, "CreateSecret with an existing name", "The operation failed because the secret "+secretName+" already exists.")
		missing := name("missing")
		_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(missing)})
		errAs[*types.ResourceNotFoundException](t, err, "GetSecretValue of a missing secret")
		wantStatus(t, err, "GetSecretValue of a missing secret", 400)
		wantMessage(t, err, "GetSecretValue of a missing secret", "Secrets Manager can't find the specified secret.")
		_, err = client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String("bad name"), SecretString: aws.String("x")})
		apiErr(t, err, "CreateSecret with a space in the name", "ValidationException")
		_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName), VersionId: aws.String("00000000-0000-0000-0000-000000000000")})
		errAs[*types.ResourceNotFoundException](t, err, "GetSecretValue with an unknown VersionId")
		wantMessage(t, err, "GetSecretValue with an unknown VersionId", "Secrets Manager can't find the specified secret value for VersionId: 00000000-0000-0000-0000-000000000000")
		_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName), VersionStage: aws.String("AWSPREVIOUS")})
		errAs[*types.ResourceNotFoundException](t, err, "GetSecretValue of a stage no version holds")
		wantMessage(t, err, "GetSecretValue AWSPREVIOUS", "Secrets Manager can't find the specified secret value for staging label: AWSPREVIOUS")
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagResource(ctx, &secretsmanager.TagResourceInput{SecretId: aws.String(secretName), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagResource")
		_, err = client.UntagResource(ctx, &secretsmanager.UntagResourceInput{SecretId: aws.String(secretName), TagKeys: []string{"team", "env"}})
		ok(t, err, "UntagResource")
		out, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
		ok(t, err, "DescribeSecret")
		if out.Tags == nil || len(out.Tags) != 0 {
			t.Errorf("Tags after removing the last tag = %#v, want present and empty", out.Tags)
		}
	})

	t.Run("resource policy", func(t *testing.T) {
		empty, err := client.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(secretName)})
		ok(t, err, "GetResourcePolicy without a policy")
		if empty.ResourcePolicy != nil {
			t.Errorf("GetResourcePolicy without a policy = %q, want the field absent", aws.ToString(empty.ResourcePolicy))
		}
		policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + acct + `:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
		put, err := client.PutResourcePolicy(ctx, &secretsmanager.PutResourcePolicyInput{SecretId: aws.String(secretName), ResourcePolicy: aws.String(policy)})
		ok(t, err, "PutResourcePolicy")
		if aws.ToString(put.ARN) != arn || aws.ToString(put.Name) != secretName {
			t.Errorf("PutResourcePolicy = ARN %q, Name %q", aws.ToString(put.ARN), aws.ToString(put.Name))
		}
		got, err := client.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(secretName)})
		ok(t, err, "GetResourcePolicy")
		wantJSONEqual(t, "ResourcePolicy", aws.ToString(got.ResourcePolicy), policy)
		_, err = client.DeleteResourcePolicy(ctx, &secretsmanager.DeleteResourcePolicyInput{SecretId: aws.String(secretName)})
		ok(t, err, "DeleteResourcePolicy")
		got, err = client.GetResourcePolicy(ctx, &secretsmanager.GetResourcePolicyInput{SecretId: aws.String(secretName)})
		ok(t, err, "GetResourcePolicy after delete")
		if got.ResourcePolicy != nil {
			t.Errorf("GetResourcePolicy after delete = %q, want absent", aws.ToString(got.ResourcePolicy))
		}
	})

	t.Run("ListSecrets", func(t *testing.T) {
		out, err := client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{Filters: []types.Filter{{Key: types.FilterNameStringTypeName, Values: []string{secretName}}}})
		ok(t, err, "ListSecrets name filter")
		if len(out.SecretList) != 1 || aws.ToString(out.SecretList[0].ARN) != arn {
			t.Fatalf("ListSecrets name filter = %+v, want just %s", out.SecretList, arn)
		}
		if _, has := out.SecretList[0].SecretVersionsToStages[v1]; !has {
			t.Errorf("ListSecrets entry SecretVersionsToStages = %v, want %s", out.SecretList[0].SecretVersionsToStages, v1)
		}
	})
}

func TestSecretsManagerVersions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := secretsmanager.NewFromConfig(awsCfg)
	secretName := name("versions")
	v1 := aws.ToString(createSecret(t, secretName, "one").VersionId)

	put2, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("two")})
	ok(t, err, "PutSecretValue two")
	v2 := aws.ToString(put2.VersionId)
	if !slices.Equal(put2.VersionStages, []string{"AWSCURRENT"}) || v2 == v1 {
		t.Errorf("PutSecretValue = VersionId %q, stages %v; want a new version holding AWSCURRENT", v2, put2.VersionStages)
	}
	if got := stagesOf(t, client, secretName, v1); !slices.Equal(got, []string{"AWSPREVIOUS"}) {
		t.Errorf("v1 stages after the second put = %v, want [AWSPREVIOUS]", got)
	}
	prev, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName), VersionStage: aws.String("AWSPREVIOUS")})
	ok(t, err, "GetSecretValue AWSPREVIOUS")
	if aws.ToString(prev.SecretString) != "one" {
		t.Errorf("AWSPREVIOUS = %q, want one", aws.ToString(prev.SecretString))
	}

	put3, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("three")})
	ok(t, err, "PutSecretValue three")
	v3 := aws.ToString(put3.VersionId)

	t.Run("deprecated versions", func(t *testing.T) {
		list, err := client.ListSecretVersionIds(ctx, &secretsmanager.ListSecretVersionIdsInput{SecretId: aws.String(secretName)})
		ok(t, err, "ListSecretVersionIds")
		var ids []string
		for _, v := range list.Versions {
			ids = append(ids, aws.ToString(v.VersionId))
		}
		if slices.Contains(ids, v1) || !slices.Contains(ids, v2) || !slices.Contains(ids, v3) {
			t.Errorf("ListSecretVersionIds = %v, want v2 and v3 but not the unlabeled v1 %s", ids, v1)
		}
		all, err := client.ListSecretVersionIds(ctx, &secretsmanager.ListSecretVersionIdsInput{SecretId: aws.String(secretName), IncludeDeprecated: aws.Bool(true)})
		ok(t, err, "ListSecretVersionIds IncludeDeprecated")
		i := slices.IndexFunc(all.Versions, func(v types.SecretVersionsListEntry) bool { return aws.ToString(v.VersionId) == v1 })
		if i < 0 {
			t.Fatalf("ListSecretVersionIds IncludeDeprecated does not list v1")
		}
		if all.Versions[i].VersionStages != nil {
			t.Errorf("unlabeled v1 VersionStages = %v, want the key absent", all.Versions[i].VersionStages)
		}
	})

	t.Run("custom stages and AWSPENDING", func(t *testing.T) {
		pending, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("pending"), VersionStages: []string{"AWSPENDING"}})
		ok(t, err, "PutSecretValue AWSPENDING")
		vp := aws.ToString(pending.VersionId)
		if !slices.Equal(pending.VersionStages, []string{"AWSPENDING"}) {
			t.Errorf("PutSecretValue AWSPENDING stages = %v", pending.VersionStages)
		}
		cur, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
		ok(t, err, "GetSecretValue")
		if aws.ToString(cur.SecretString) != "three" {
			t.Errorf("AWSCURRENT after an AWSPENDING put = %q, want three (unchanged)", aws.ToString(cur.SecretString))
		}
		_, err = client.UpdateSecretVersionStage(ctx, &secretsmanager.UpdateSecretVersionStageInput{
			SecretId: aws.String(secretName), VersionStage: aws.String("AWSCURRENT"), MoveToVersionId: aws.String(vp), RemoveFromVersionId: aws.String(v3),
		})
		ok(t, err, "UpdateSecretVersionStage AWSCURRENT to the pending version")
		if got := stagesOf(t, client, secretName, vp); !slices.Equal(got, []string{"AWSCURRENT", "AWSPENDING"}) {
			t.Errorf("pending version stages = %v, want [AWSCURRENT AWSPENDING]", got)
		}
		if got := stagesOf(t, client, secretName, v3); !slices.Equal(got, []string{"AWSPREVIOUS"}) {
			t.Errorf("v3 stages = %v, want [AWSPREVIOUS]", got)
		}
		custom, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("custom"), VersionStages: []string{"two", "one", "three"}})
		ok(t, err, "PutSecretValue with custom stages")
		if !slices.Equal(custom.VersionStages, []string{"one", "three", "two"}) {
			t.Errorf("custom stages = %v, want sorted [one three two]", custom.VersionStages)
		}
		_, err = client.UpdateSecretVersionStage(ctx, &secretsmanager.UpdateSecretVersionStageInput{SecretId: aws.String(secretName), VersionStage: aws.String("AWSCURRENT"), RemoveFromVersionId: aws.String(vp)})
		apiErr(t, err, "removing AWSCURRENT without moving it", "InvalidParameterException")
	})

	t.Run("idempotency", func(t *testing.T) {
		token := "11111111-2222-3333-4444-" + strings.Repeat("5", 12)
		first, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("same"), ClientRequestToken: aws.String(token)})
		ok(t, err, "PutSecretValue with a token")
		again, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("same"), ClientRequestToken: aws.String(token)})
		ok(t, err, "PutSecretValue replay with the same token and value")
		if aws.ToString(again.VersionId) != token || aws.ToString(first.VersionId) != token {
			t.Errorf("VersionIds %q, %q; want the ClientRequestToken %q", aws.ToString(first.VersionId), aws.ToString(again.VersionId), token)
		}
		_, err = client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String("different"), ClientRequestToken: aws.String(token)})
		if err == nil {
			t.Errorf("PutSecretValue reusing a token with a different value succeeded; AWS fails it because a version cannot be modified")
		}
	})

	t.Run("UpdateSecret", func(t *testing.T) {
		desc, err := client.UpdateSecret(ctx, &secretsmanager.UpdateSecretInput{SecretId: aws.String(secretName), Description: aws.String("new description")})
		ok(t, err, "UpdateSecret description only")
		if desc.VersionId != nil {
			t.Errorf("UpdateSecret without a value returned VersionId %q, want none", aws.ToString(desc.VersionId))
		}
		val, err := client.UpdateSecret(ctx, &secretsmanager.UpdateSecretInput{SecretId: aws.String(secretName), SecretString: aws.String("updated")})
		ok(t, err, "UpdateSecret with a value")
		if val.VersionId == nil {
			t.Errorf("UpdateSecret with a value returned no VersionId")
		}
		cur, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
		ok(t, err, "GetSecretValue")
		if aws.ToString(cur.SecretString) != "updated" || aws.ToString(cur.VersionId) != aws.ToString(val.VersionId) {
			t.Errorf("AWSCURRENT = %q (%q), want the updated value", aws.ToString(cur.SecretString), aws.ToString(cur.VersionId))
		}
	})
}

func TestSecretsManagerEmptyAndBinary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := secretsmanager.NewFromConfig(awsCfg)
	secretName := name("empty")
	out, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String(secretName)})
	ok(t, err, "CreateSecret without a value")
	t.Cleanup(func() {
		_, _ = client.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: out.ARN, ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	if out.VersionId != nil {
		t.Errorf("CreateSecret without a value returned VersionId %q", aws.ToString(out.VersionId))
	}
	_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	errAs[*types.ResourceNotFoundException](t, err, "GetSecretValue of an empty secret")
	wantMessage(t, err, "GetSecretValue of an empty secret", "Secrets Manager can't find the specified secret value for staging label: AWSCURRENT")
	put, err := client.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretBinary: []byte{0, 1, 2, 255}})
	ok(t, err, "PutSecretValue binary")
	if !slices.Equal(put.VersionStages, []string{"AWSCURRENT"}) {
		t.Errorf("first PutSecretValue stages = %v, want [AWSCURRENT]", put.VersionStages)
	}
	got, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	ok(t, err, "GetSecretValue binary")
	if !slices.Equal(got.SecretBinary, []byte{0, 1, 2, 255}) || got.SecretString != nil {
		t.Errorf("GetSecretValue = binary %v, string %v; want the bytes and no string", got.SecretBinary, got.SecretString)
	}
}

func TestSecretsManagerDeletion(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := secretsmanager.NewFromConfig(awsCfg)
	secretName := name("deleted")
	arn := aws.ToString(createSecret(t, secretName, "value").ARN)

	_, err := client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(secretName), RecoveryWindowInDays: aws.Int64(3)})
	apiErr(t, err, "DeleteSecret with a 3-day window", "InvalidParameterException")

	before := time.Now()
	del, err := client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(secretName), RecoveryWindowInDays: aws.Int64(7)})
	ok(t, err, "DeleteSecret with a 7-day window")
	if aws.ToString(del.ARN) != arn || del.DeletionDate == nil || del.DeletionDate.Sub(before) < 7*24*time.Hour-time.Minute || del.DeletionDate.Sub(before) > 7*24*time.Hour+time.Minute {
		t.Errorf("DeleteSecret = ARN %q, DeletionDate %v; want about now + 7 days", aws.ToString(del.ARN), del.DeletionDate)
	}

	desc, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
	ok(t, err, "DescribeSecret of a secret scheduled for deletion")
	if desc.DeletedDate == nil {
		t.Errorf("DescribeSecret DeletedDate is absent for a secret scheduled for deletion")
	}
	_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	errAs[*types.InvalidRequestException](t, err, "GetSecretValue of a secret scheduled for deletion")
	wantMessageContains(t, err, "GetSecretValue of a secret scheduled for deletion", "because it was marked for deletion")
	_, err = client.TagResource(ctx, &secretsmanager.TagResourceInput{SecretId: aws.String(secretName), Tags: []types.Tag{{Key: aws.String("k"), Value: aws.String("v")}}})
	errAs[*types.InvalidRequestException](t, err, "TagResource on a secret scheduled for deletion")
	wantMessage(t, err, "TagResource on a deleted secret", "You can't perform this operation on the secret because it was marked for deletion.")
	_, err = client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String(secretName), SecretString: aws.String("x")})
	errAs[*types.InvalidRequestException](t, err, "CreateSecret with the name of a secret scheduled for deletion")
	wantMessage(t, err, "CreateSecret during the deletion window", "You can't create this secret because a secret with this name is already scheduled for deletion.")

	list, err := client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{Filters: []types.Filter{{Key: types.FilterNameStringTypeName, Values: []string{secretName}}}})
	ok(t, err, "ListSecrets")
	if len(list.SecretList) != 0 {
		t.Errorf("ListSecrets lists a secret scheduled for deletion without IncludePlannedDeletion")
	}
	list, err = client.ListSecrets(ctx, &secretsmanager.ListSecretsInput{IncludePlannedDeletion: aws.Bool(true), Filters: []types.Filter{{Key: types.FilterNameStringTypeName, Values: []string{secretName}}}})
	ok(t, err, "ListSecrets IncludePlannedDeletion")
	if len(list.SecretList) != 1 {
		t.Errorf("ListSecrets IncludePlannedDeletion = %d entries, want 1", len(list.SecretList))
	}

	restore, err := client.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{SecretId: aws.String(secretName)})
	ok(t, err, "RestoreSecret")
	if aws.ToString(restore.ARN) != arn {
		t.Errorf("RestoreSecret ARN = %q", aws.ToString(restore.ARN))
	}
	got, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	ok(t, err, "GetSecretValue after restore")
	if aws.ToString(got.SecretString) != "value" {
		t.Errorf("value after restore = %q", aws.ToString(got.SecretString))
	}

	force, err := client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(secretName), ForceDeleteWithoutRecovery: aws.Bool(true)})
	ok(t, err, "DeleteSecret ForceDeleteWithoutRecovery")
	if force.DeletionDate == nil {
		t.Errorf("force delete returned no DeletionDate")
	}
	_, err = client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
	errAs[*types.ResourceNotFoundException](t, err, "DescribeSecret after a force delete")
	again := createSecret(t, secretName, "reborn")
	if aws.ToString(again.ARN) == arn {
		t.Errorf("a recreated secret reuses the ARN %q; AWS gives it a new random suffix", arn)
	}
	_, err = client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{SecretId: aws.String(name("never")), ForceDeleteWithoutRecovery: aws.Bool(true)})
	ok(t, err, "force-deleting a secret that does not exist (no ResourceNotFoundException)")
}

func TestSecretsManagerKMS(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := secretsmanager.NewFromConfig(awsCfg)
	key := createKey(t, "secrets manager conformance")
	secretName := name("kms")
	out, err := client.CreateSecret(ctx, &secretsmanager.CreateSecretInput{Name: aws.String(secretName), SecretString: aws.String("encrypted"), KmsKeyId: key.KeyId})
	ok(t, err, "CreateSecret with a customer key")
	t.Cleanup(func() {
		_, _ = client.DeleteSecret(context.Background(), &secretsmanager.DeleteSecretInput{SecretId: out.ARN, ForceDeleteWithoutRecovery: aws.Bool(true)})
	})
	desc, err := client.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
	ok(t, err, "DescribeSecret")
	if desc.KmsKeyId == nil {
		t.Errorf("DescribeSecret KmsKeyId is absent for a secret under a customer key")
	}
	got, err := client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	ok(t, err, "GetSecretValue")
	if aws.ToString(got.SecretString) != "encrypted" {
		t.Errorf("GetSecretValue = %q", aws.ToString(got.SecretString))
	}
	_, err = kms.NewFromConfig(awsCfg).DisableKey(ctx, &kms.DisableKeyInput{KeyId: key.KeyId})
	ok(t, err, "kms DisableKey")
	_, err = client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	errAs[*types.DecryptionFailure](t, err, "GetSecretValue with the key disabled")
}
