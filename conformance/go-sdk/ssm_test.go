package gosdk_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// Research: docs/research/ssm-parameters.md; recorded shapes from
// LocalStack's AWS-validated snapshots (localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json).

// putParameter writes a parameter and deletes it when the test ends.
func putParameter(t *testing.T, client *ssm.Client, in *ssm.PutParameterInput) *ssm.PutParameterOutput {
	t.Helper()
	out, err := client.PutParameter(t.Context(), in)
	ok(t, err, "PutParameter "+aws.ToString(in.Name))
	t.Cleanup(func() {
		_, _ = client.DeleteParameter(context.Background(), &ssm.DeleteParameterInput{Name: in.Name})
	})
	return out
}

func TestSSMStringLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ssm.NewFromConfig(awsCfg)
	base := "/" + name(t, "conf-ssm")
	p := base + "/string"
	arn := fmt.Sprintf("arn:aws:ssm:%s:%s:parameter%s", region(), account(t), p)

	// test_ssm.snapshot.json:L19-L26: a plain create answers Standard, 1.
	put := putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("one"), Type: types.ParameterTypeString})
	if put.Tier != types.ParameterTierStandard || put.Version != 1 {
		t.Errorf("PutParameter = Tier %q Version %d, want Standard 1", put.Tier, put.Version)
	}

	// test_ssm.snapshot.json:L27-L56: by name and by ARN, the name as written.
	for _, ask := range []string{p, arn} {
		got, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(ask)})
		ok(t, err, "GetParameter "+ask)
		if pr := got.Parameter; aws.ToString(pr.Name) != p || aws.ToString(pr.Value) != "one" || pr.Version != 1 || aws.ToString(pr.ARN) != arn || aws.ToString(pr.DataType) != "text" || pr.Selector != nil {
			t.Errorf("GetParameter %s = %+v", ask, pr)
		}
	}

	// ssm-parameters.md 7.1: without Overwrite an existing name is refused.
	_, err := client.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("two"), Type: types.ParameterTypeString})
	errAs[*types.ParameterAlreadyExists](t, err, "PutParameter without Overwrite")
	wantStatus(t, err, "PutParameter without Overwrite", 400)

	// ssm-parameters.md 6.1: an overwrite is version 2.
	over, err := client.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("two"), Overwrite: aws.Bool(true)})
	ok(t, err, "PutParameter with Overwrite")
	if over.Version != 2 {
		t.Errorf("overwrite Version = %d, want 2", over.Version)
	}
	got, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p + ":1")})
	ok(t, err, "GetParameter :1")
	if aws.ToString(got.Parameter.Value) != "one" || aws.ToString(got.Parameter.Selector) != ":1" {
		t.Errorf("GetParameter :1 = %+v, want one with Selector :1 (ssm-parameters.md 6.3)", got.Parameter)
	}

	// ssm-parameters.md 7.7 and G7: delete, then ParameterNotFound, 400, "".
	_, err = client.DeleteParameter(ctx, &ssm.DeleteParameterInput{Name: aws.String(p)})
	ok(t, err, "DeleteParameter")
	_, err = client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p)})
	errAs[*types.ParameterNotFound](t, err, "GetParameter after delete")
	wantStatus(t, err, "GetParameter after delete", 400)
	wantMessage(t, err, "GetParameter after delete", "")
}

// ssm-parameters.md 5.2, 5.3: a SecureString read without decryption is
// the base64 KMS ciphertext, which KMS Decrypt opens with
// PARAMETER_ARN=<parameter ARN>; with decryption it is the plaintext.
func TestSSMSecureString(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ssm.NewFromConfig(awsCfg)
	p := "/" + name(t, "conf-ssm") + "/secure"
	putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("hunter2"), Type: types.ParameterTypeSecureString})

	plain, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p), WithDecryption: aws.Bool(true)})
	ok(t, err, "GetParameter with decryption")
	if aws.ToString(plain.Parameter.Value) != "hunter2" || plain.Parameter.Type != types.ParameterTypeSecureString {
		t.Errorf("with decryption = %+v", plain.Parameter)
	}
	sealed, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p)})
	ok(t, err, "GetParameter without decryption")
	if aws.ToString(sealed.Parameter.Value) == "hunter2" {
		t.Errorf("without decryption = %+v", sealed.Parameter)
	}

	// A customer managed key, because the aws/ssm key's policy allows Decrypt
	// only through SSM (ssm-parameters.md 5.6).
	keyed := p + "-cmk"
	arn := fmt.Sprintf("arn:aws:ssm:%s:%s:parameter%s", region(), account(t), keyed)
	key := createKey(t, "conformance ssm key")
	putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(keyed), Value: aws.String("hunter2"), Type: types.ParameterTypeSecureString, KeyId: key.KeyId})
	sealed, err = client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(keyed)})
	ok(t, err, "GetParameter without decryption, customer managed key")
	blob, err := base64.StdEncoding.DecodeString(aws.ToString(sealed.Parameter.Value))
	ok(t, err, "base64 of the value read without decryption")
	opened, err := kms.NewFromConfig(awsCfg).Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: blob, EncryptionContext: map[string]string{"PARAMETER_ARN": arn}})
	ok(t, err, "KMS Decrypt with PARAMETER_ARN")
	if string(opened.Plaintext) != "hunter2" {
		t.Errorf("KMS Decrypt = %q, want hunter2", opened.Plaintext)
	}

	// ssm-parameters.md 5.4: the default key reads back alias/aws/ssm.
	described, err := client.DescribeParameters(ctx, &ssm.DescribeParametersInput{ParameterFilters: []types.ParameterStringFilter{{Key: aws.String("Name"), Option: aws.String("Equals"), Values: []string{p}}}})
	ok(t, err, "DescribeParameters")
	if len(described.Parameters) != 1 || aws.ToString(described.Parameters[0].KeyId) != "alias/aws/ssm" {
		t.Errorf("DescribeParameters = %+v, want one entry with KeyId alias/aws/ssm", described.Parameters)
	}
}

// ssm-parameters.md 7.3 and 7.4: GetParameters lists a missing name in
// InvalidParameters; GetParametersByPath with Recursive pages through every
// parameter under the path.
func TestSSMBatchAndPathReads(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ssm.NewFromConfig(awsCfg)
	base := "/" + name(t, "conf-ssm")
	want := make([]string, 0, 5)
	for i := range 5 {
		p := fmt.Sprintf("%s/l%d/p%d", base, i%2, i)
		putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("v"), Type: types.ParameterTypeString})
		want = append(want, p)
	}
	slices.Sort(want)

	batch, err := client.GetParameters(ctx, &ssm.GetParametersInput{Names: []string{want[0], base + "/missing"}})
	ok(t, err, "GetParameters")
	if len(batch.Parameters) != 1 || !slices.Equal(batch.InvalidParameters, []string{base + "/missing"}) {
		t.Errorf("GetParameters = %d found, InvalidParameters %v", len(batch.Parameters), batch.InvalidParameters)
	}

	var got []string
	pages := ssm.NewGetParametersByPathPaginator(client, &ssm.GetParametersByPathInput{Path: aws.String(base), Recursive: aws.Bool(true), MaxResults: aws.Int32(2)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		ok(t, err, "GetParametersByPath page")
		for _, p := range page.Parameters {
			got = append(got, aws.ToString(p.Name))
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("GetParametersByPath recursive = %v, want %v", got, want)
	}
}

// ssm-parameters.md 6.2, 7.6, 7.8: history oldest first, labels as
// selectors, and unlabel.
func TestSSMHistoryAndLabels(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ssm.NewFromConfig(awsCfg)
	p := "/" + name(t, "conf-ssm") + "/labeled"
	putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("v1"), Type: types.ParameterTypeString})
	_, err := client.PutParameter(ctx, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("v2"), Overwrite: aws.Bool(true)})
	ok(t, err, "overwrite")

	label, err := client.LabelParameterVersion(ctx, &ssm.LabelParameterVersionInput{Name: aws.String(p), ParameterVersion: aws.Int64(1), Labels: []string{"stable", "1bad"}})
	ok(t, err, "LabelParameterVersion")
	if label.ParameterVersion != 1 || !slices.Equal(label.InvalidLabels, []string{"1bad"}) {
		t.Errorf("LabelParameterVersion = %+v, want version 1 and InvalidLabels [1bad]", label)
	}
	got, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(p + ":stable")})
	ok(t, err, "GetParameter :stable")
	if aws.ToString(got.Parameter.Value) != "v1" || aws.ToString(got.Parameter.Selector) != ":stable" {
		t.Errorf("GetParameter :stable = %+v", got.Parameter)
	}

	hist, err := client.GetParameterHistory(ctx, &ssm.GetParameterHistoryInput{Name: aws.String(p)})
	ok(t, err, "GetParameterHistory")
	if len(hist.Parameters) != 2 || hist.Parameters[0].Version != 1 || hist.Parameters[1].Version != 2 || !slices.Equal(hist.Parameters[0].Labels, []string{"stable"}) {
		t.Errorf("GetParameterHistory = %+v, want versions 1 and 2 with stable on 1", hist.Parameters)
	}

	unlabel, err := client.UnlabelParameterVersion(ctx, &ssm.UnlabelParameterVersionInput{Name: aws.String(p), ParameterVersion: aws.Int64(1), Labels: []string{"stable", "absent"}})
	ok(t, err, "UnlabelParameterVersion")
	if !slices.Equal(unlabel.RemovedLabels, []string{"stable"}) || !slices.Equal(unlabel.InvalidLabels, []string{"absent"}) {
		t.Errorf("UnlabelParameterVersion = %+v", unlabel)
	}
}

// ssm-parameters.md 7.9: tags on the Parameter resource type by name.
func TestSSMTags(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := ssm.NewFromConfig(awsCfg)
	p := "/" + name(t, "conf-ssm") + "/tagged"
	putParameter(t, client, &ssm.PutParameterInput{Name: aws.String(p), Value: aws.String("v"), Type: types.ParameterTypeString, Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("dev")}}})
	_, err := client.AddTagsToResource(ctx, &ssm.AddTagsToResourceInput{ResourceType: types.ResourceTypeForTaggingParameter, ResourceId: aws.String(p), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("prod")}, {Key: aws.String("team"), Value: aws.String("a")}}})
	ok(t, err, "AddTagsToResource")
	_, err = client.RemoveTagsFromResource(ctx, &ssm.RemoveTagsFromResourceInput{ResourceType: types.ResourceTypeForTaggingParameter, ResourceId: aws.String(p), TagKeys: []string{"team"}})
	ok(t, err, "RemoveTagsFromResource")
	list, err := client.ListTagsForResource(ctx, &ssm.ListTagsForResourceInput{ResourceType: types.ResourceTypeForTaggingParameter, ResourceId: aws.String(p)})
	ok(t, err, "ListTagsForResource")
	if len(list.TagList) != 1 || aws.ToString(list.TagList[0].Key) != "env" || aws.ToString(list.TagList[0].Value) != "prod" {
		t.Errorf("TagList = %+v, want env=prod", list.TagList)
	}
}
