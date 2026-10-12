package e2e_test

import (
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// currentVersion is the version id holding AWSCURRENT, and the stages of
// every version.
func currentVersion(t *testing.T) (current string, stages map[string][]string) {
	t.Helper()
	d, err := env.secrets.DescribeSecret(t.Context(), &secretsmanager.DescribeSecretInput{SecretId: aws.String(secretName)})
	if err != nil {
		t.Fatalf("secretsmanager DescribeSecret: %v", err)
	}
	for v, s := range d.VersionIdsToStages {
		if slices.Contains(s, "AWSCURRENT") {
			current = v
		}
	}
	if current == "" {
		t.Fatalf("no version of %s holds AWSCURRENT: %v", secretName, d.VersionIdsToStages)
	}
	return current, d.VersionIdsToStages
}

// TestSecretsManagerWorkersReadTheCurrentVersion checks the workers read
// the AWSCURRENT version, and that PutSecretValue moves AWSCURRENT to the
// new version and AWSPREVIOUS to the old one, which the next message then
// sees (secrets-logs-events-alarms.md, Secrets Manager versions and stages).
func TestSecretsManagerWorkersReadTheCurrentVersion(t *testing.T) {
	pipe(t)
	before, _ := currentVersion(t)
	first := newID(t)
	publish(t, message{ID: first}, map[string]string{"kind": "order"})
	if got := waitProcessed(t, goWorker, first).SecretVersionID; got != before {
		t.Errorf("the worker read version %q, want AWSCURRENT %s", got, before)
	}

	put, err := env.secrets.PutSecretValue(t.Context(), &secretsmanager.PutSecretValueInput{SecretId: aws.String(secretName), SecretString: aws.String(`{"token":"t-2"}`)})
	if err != nil {
		t.Fatalf("secretsmanager PutSecretValue: %v", err)
	}
	after, stages := currentVersion(t)
	if after != aws.ToString(put.VersionId) || !slices.Equal(put.VersionStages, []string{"AWSCURRENT"}) || !slices.Contains(stages[before], "AWSPREVIOUS") {
		t.Errorf("after PutSecretValue: AWSCURRENT on %s, response stages %v, stages %v; want AWSCURRENT on the new version %s and AWSPREVIOUS on %s",
			after, put.VersionStages, stages, aws.ToString(put.VersionId), before)
	}
	got, err := env.secrets.GetSecretValue(t.Context(), &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	if err != nil {
		t.Fatalf("secretsmanager GetSecretValue: %v", err)
	}
	if aws.ToString(got.SecretString) != `{"token":"t-2"}` || aws.ToString(got.VersionId) != after {
		t.Errorf("GetSecretValue %q at %s, want the new value at %s", aws.ToString(got.SecretString), aws.ToString(got.VersionId), after)
	}

	second := newID(t)
	publish(t, message{ID: second}, map[string]string{"kind": "order"})
	if got := waitProcessed(t, goWorker, second).SecretVersionID; got != after {
		t.Errorf("after the update the worker read version %q, want the new AWSCURRENT %s", got, after)
	}
}
