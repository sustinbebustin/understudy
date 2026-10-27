package ssm_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// The texts AWS gives for the overwrite refusals (ssm-parameters.md 7.1,
// 12.4; [U] hashicorp/terraform-provider-aws#18550; tier [T]
// parameter.go:L181-L185).
const (
	alreadyExists   = "The parameter already exists. To overwrite this value, set the overwrite option in the request to true."
	tagsAndOverride = "Invalid request: tags and overwrite can't be used together. To create a parameter with tags, please remove overwrite flag. To update tags for an existing parameter, please use AddTagsToResource or RemoveTagsFromResource."
	noDowngrade     = "This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. If necessary, you can delete the advanced parameter and recreate it as a standard parameter."
)

// A create answers version 1 and DataType text; a create of an existing
// name without Overwrite answers ParameterAlreadyExists with AWS's text and
// changes nothing; with Overwrite the value becomes version 2 ("Parameter
// Store automatically increments the version number by one",
// ssm-parameters.md 6.1, aws-doc sysman-paramstore-versions.html).
func TestCreateAndOverwrite(t *testing.T) {
	run(t, func(h *harness) {
		if out := h.put("/app/a", "one"); out.Version != 1 {
			h.Errorf("create Version = %d, want 1", out.Version)
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/app/a", Value: "two", Type: new(ssmapi.ParameterTypeString)})
		wantError(h.T, "PutParameter of an existing name", err, ssmapi.IdentityParameterAlreadyExists(), alreadyExists)
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/app/a", Value: "two", Type: new(ssmapi.ParameterTypeString), Overwrite: new(false)})
		wantError(h.T, "PutParameter of an existing name with Overwrite false", err, ssmapi.IdentityParameterAlreadyExists(), alreadyExists)
		if got := h.mustGet("/app/a", false); val(got.Value) != "one" || got.Version != 1 {
			h.Errorf("after the refusals = %q version %d, want one 1", val(got.Value), got.Version)
		}
		if out := h.overwrite("/app/a", "two"); out.Version != 2 {
			h.Errorf("overwrite Version = %d, want 2", out.Version)
		}
		got := h.mustGet("/app/a", false)
		if val(got.Value) != "two" || got.Version != 2 || val(got.DataType) != "text" || val(got.Type) != ssmapi.ParameterTypeString {
			h.Errorf("after the overwrite = %+v, want two, version 2, DataType text, String", got)
		}
	})
}

// Against AWS a description-only overwrite creates version 2 (Terraform
// acceptance test, ssm-parameters.md 6.1 [T] parameter_test.go:L160-L206).
func TestDescriptionOnlyOverwriteCreatesAVersion(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/d/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Description: new("first")})
		out := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/d/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Description: new("second"), Overwrite: new(true)})
		if out.Version != 2 {
			h.Errorf("description-only overwrite Version = %d, want 2", out.Version)
		}
		if d := h.mustDescribeOne("/d/a"); val(d.Description) != "second" || d.Version != 2 {
			h.Errorf("DescribeParameters = Description %q Version %d, want second 2", val(d.Description), d.Version)
		}
	})
}

// Tags with Overwrite: true are refused with AWS's text (ssm-parameters.md
// 7.1, 12.4 [U]), and the parameter keeps its value and tags. Tags on a
// create without Overwrite are stored (12.5 item 1).
func TestTagsAndOverwrite(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/t/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Overwrite: new(false),
			Tags: []ssmapi.Tag{{Key: "env", Value: "dev"}},
		})
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/t/a", Value: "w", Overwrite: new(true), Tags: []ssmapi.Tag{{Key: "env", Value: "prod"}},
		})
		wantError(h.T, "PutParameter with Tags and Overwrite", err, ssmapi.IdentityValidationException(), tagsAndOverride)
		if got := h.mustGet("/t/a", false); val(got.Value) != "v" || got.Version != 1 {
			h.Errorf("after the refusal = %q version %d, want v 1", val(got.Value), got.Version)
		}
		tags := must(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "/t/a"}).TagList
		if len(tags) != 1 || tags[0].Key != "env" || tags[0].Value != "dev" {
			h.Errorf("TagList = %+v, want env=dev", tags)
		}
	})
}

// "You can't include values wrapped in double brackets {{}} or
// {{ssm:parameter-name}} in a parameter value" (ssm-parameters.md 4.1,
// aws-doc API_PutParameter): such a value is refused as a client error and
// nothing is stored; the code is understudy's choice (SSM-11 step 05), so
// only the refusal is asserted, and the message never repeats the value
// (spec "Coverage marks").
func TestDoubleBracesAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		for _, v := range []string{"{{}}", "x{{ssm:other}}y"} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/braces/a", Value: v, Type: new(ssmapi.ParameterTypeString)})
			if err == nil || err.ID.Status != 400 {
				h.Errorf("PutParameter of %q = %v, want a 400 refusal", v, err)
				continue
			}
			wantOwnMessage(h.T, "PutParameter of "+v, err, v)
		}
		if err := h.get("/braces/a"); err == nil {
			h.Error("a refused value was stored")
		}
	})
}

// DataType takes text and aws:ssm:integration (ssm-parameters.md 4.3,
// aws-doc API_PutParameter; the integration type with a SecureString under
// the provider's webhook path, [T] parameter_test.go:L1225-L1252); any other
// value is SSM's ValidationException (contract 4.1 row 6), and
// aws:ec2:image answers 501 since understudy has no EC2 to validate an AMI
// (spec "Scope").
func TestDataType(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/dt/text", Value: "v", Type: new(ssmapi.ParameterTypeString), DataType: new("text")})
		integration := "/d9d01087-4a3f-49e0-b0b4-d568d7826553/ssm/integrations/webhook/hook"
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: integration, Value: `{"description":"d","url":"https://example.com"}`, Type: new(ssmapi.ParameterTypeSecureString), DataType: new("aws:ssm:integration")})
		if d := h.mustDescribeOne(integration); val(d.DataType) != "aws:ssm:integration" {
			h.Errorf("DataType = %q, want aws:ssm:integration", val(d.DataType))
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/dt/bad", Value: "v", Type: new(ssmapi.ParameterTypeString), DataType: new("Text")})
		wantCode(h.T, "DataType Text", err, ssmapi.IdentityValidationException())
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/dt/ami", Value: "ami-12345abcde", Type: new(ssmapi.ParameterTypeString), DataType: new("aws:ec2:image")})
		wantNotImplemented(h.T, "DataType aws:ec2:image", err, "aws:ec2:image")
		for _, name := range []string{"/dt/bad", "/dt/ami"} {
			if err := h.get(name); err == nil {
				h.Errorf("%s was stored", name)
			}
		}
	})
}

// Parameter policies answer 501 (spec "Scope"; ssm-parameters.md 12.1).
func TestPoliciesAnswer501(t *testing.T) {
	run(t, func(h *harness) {
		policy := `[{"Type":"Expiration","Version":"1.0","Attributes":{"Timestamp":"2030-01-01T00:00:00.000Z"}}]`
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/pol/a", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierAdvanced), Policies: &policy})
		wantNotImplemented(h.T, "PutParameter with Policies", err, "Policies")
		if err := h.get("/pol/a"); err == nil {
			h.Error("a parameter with policies was stored")
		}
	})
}

// AllowedPattern is matched against the whole value: the user guide shows
// 10,000 refused by \d{1,4} with ParameterPatternMismatchException and the
// text "Parameter value, cannot be validated against allowedPattern:
// \d{1,4}" although 10,000 holds a run of 1 to 4 digits (ssm-parameters.md
// 4.4, aws-doc sysman-paramstore-hierarchies.html). An empty pattern is no
// pattern: Terraform sends "" whenever allowed_pattern is unset (4.4 [T]
// parameter.go:L227).
func TestAllowedPatternIsAWholeValueMatch(t *testing.T) {
	run(t, func(h *harness) {
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/MyService/Test/MaxConnections", Value: "1000", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(`\d{1,4}`)})
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/MyService/Test/MaxConnections", Value: "10,000", Overwrite: new(true), AllowedPattern: new(`\d{1,4}`)})
		wantError(h.T, "10,000 against \\d{1,4}", err, ssmapi.IdentityParameterPatternMismatchException(), `Parameter value, cannot be validated against allowedPattern: \d{1,4}`)
		if got := h.mustGet("/MyService/Test/MaxConnections", false); val(got.Value) != "1000" || got.Version != 1 {
			h.Errorf("after the mismatch = %q version %d, want 1000 1", val(got.Value), got.Version)
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tf/unset", Value: "anything at all", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new("")})
		if d := h.mustDescribeOne("/tf/unset"); d.AllowedPattern != nil {
			h.Errorf("AllowedPattern = %q after an empty one, want absent (ssm-parameters.md 12.5 item 3)", *d.AllowedPattern)
		}
	})
}

// AllowedPattern is compiled as Go RE2: a pattern RE2 cannot compile for a
// reason other than the six structural errors answers 501 naming
// AllowedPattern (spec "Scope"; PutParameter's coverage note), and nothing
// is stored. A lookahead and a backreference are Java constructs RE2 lacks.
func TestUncompilableAllowedPatternAnswers501(t *testing.T) {
	run(t, func(h *harness) {
		for _, p := range []string{`(?=a)a`, `(a)\1`} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/re/a", Value: "a", Type: new(ssmapi.ParameterTypeString), AllowedPattern: new(p)})
			wantNotImplemented(h.T, "AllowedPattern "+p, err, "AllowedPattern")
		}
		if err := h.get("/re/a"); err == nil {
			h.Error("a parameter with an uncompilable pattern was stored")
		}
	})
}

// Under switch ssm.put.omitted-fields (preserved), an overwrite that sends
// only Name, Value and Overwrite keeps Description, KeyId, DataType, Type
// and AllowedPattern, and the kept pattern still refuses (ssm-parameters.md
// 7.1: the guide's --value 10,000 --overwrite without --allowed-pattern is
// refused citing the stored \d{1,4}, aws-doc
// sysman-paramstore-hierarchies.html).
func TestOmittedFieldsOnOverwrite(t *testing.T) {
	run(t, func(h *harness) {
		h.follows("ssm.put.omitted-fields", "preserved")
		key := h.createKey()
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{
			Name: "/keep/secret", Value: "1234", Type: new(ssmapi.ParameterTypeSecureString), KeyId: new(key),
			Description: new("kept"), AllowedPattern: new(`\d{1,4}`), DataType: new("text"),
		})
		if out := h.overwrite("/keep/secret", "5678"); out.Version != 2 {
			h.Errorf("value-only overwrite Version = %d, want 2", out.Version)
		}
		d := h.mustDescribeOne("/keep/secret")
		if val(d.Description) != "kept" || val(d.KeyId) != key || val(d.DataType) != "text" || val(d.Type) != ssmapi.ParameterTypeSecureString || val(d.AllowedPattern) != `\d{1,4}` {
			h.Errorf("after a value-only overwrite = Description %q KeyId %q DataType %q Type %q AllowedPattern %q, want everything kept",
				val(d.Description), val(d.KeyId), val(d.DataType), val(d.Type), val(d.AllowedPattern))
		}
		if got := h.mustGet("/keep/secret", true); val(got.Value) != "5678" {
			h.Errorf("decrypted value = %q, want 5678", val(got.Value))
		}
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/keep/secret", Value: "10,000", Overwrite: new(true)})
		wantError(h.T, "the kept pattern", err, ssmapi.IdentityParameterPatternMismatchException(), `Parameter value, cannot be validated against allowedPattern: \d{1,4}`)
	})
}

// Tiers (ssm-parameters.md 4.2): Intelligent-Tiering with a small value
// reads back Standard and with a 5000-character value Advanced, on create
// and on an update that grows the value ([T] parameter_test.go:L399-L556);
// an Advanced parameter refuses a Standard write with AWS's text ([T]
// parameter.go:L181-L185, 12.4) and stays Advanced. Terraform reads the
// tier from DescribeParameters (12.5 item 3), never Intelligent-Tiering.
func TestTiers(t *testing.T) {
	run(t, func(h *harness) {
		big := strings.Repeat("x", 5000)
		tier := func(name string) ssmapi.ParameterTier {
			h.Helper()
			return val(h.mustDescribeOne(name).Tier)
		}
		intelligent := new(ssmapi.ParameterTierIntelligentTiering)
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tier/small", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: intelligent})
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tier/big", Value: big, Type: new(ssmapi.ParameterTypeString), Tier: intelligent})
		if got := tier("/tier/small"); got != ssmapi.ParameterTierStandard {
			h.Errorf("Intelligent-Tiering small = %q, want Standard", got)
		}
		if got := tier("/tier/big"); got != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering 5000 characters = %q, want Advanced", got)
		}
		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tier/small", Value: big, Overwrite: new(true), Tier: intelligent})
		if got := tier("/tier/small"); got != ssmapi.ParameterTierAdvanced {
			h.Errorf("Intelligent-Tiering grown to 5000 characters = %q, want Advanced", got)
		}
		if got := h.mustGet("/tier/big", false); val(got.Value) != big {
			h.Errorf("an Advanced value of %d characters read back %d", len(big), len(val(got.Value)))
		}

		must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tier/adv", Value: "v", Type: new(ssmapi.ParameterTypeString), Tier: new(ssmapi.ParameterTierAdvanced)})
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/tier/adv", Value: "w", Overwrite: new(true), Tier: new(ssmapi.ParameterTierStandard)})
		wantError(h.T, "Advanced to Standard", err, ssmapi.IdentityValidationException(), noDowngrade)
		if got := tier("/tier/adv"); got != ssmapi.ParameterTierAdvanced {
			h.Errorf("after the refused downgrade = %q, want Advanced", got)
		}
	})
}

// An Advanced SecureString answers 501, explicit or resolved from
// Intelligent-Tiering, and nothing is stored (spec "Scope", conflict 4).
func TestAdvancedSecureStringAnswers501(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/adv/s1", Value: "v", Type: new(ssmapi.ParameterTypeSecureString), Tier: new(ssmapi.ParameterTierAdvanced)})
		wantNotImplemented(h.T, "Advanced SecureString", err, "Advanced")
		_, err = call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/adv/s2", Value: strings.Repeat("s", 5000), Type: new(ssmapi.ParameterTypeSecureString), Tier: new(ssmapi.ParameterTierIntelligentTiering)})
		wantNotImplemented(h.T, "Intelligent-Tiering SecureString of 5000 characters", err, "Advanced")
		for _, name := range []string{"/adv/s1", "/adv/s2"} {
			if err := h.get(name); err == nil {
				h.Errorf("%s was stored", name)
			}
		}
	})
}

// "After you have created 100 versions of a parameter, each time you create
// a new version, the oldest version of the parameter is removed from
// history"; when the oldest carries a label the write fails with
// ParameterMaxVersionLimitExceeded and the model's text naming the
// parameter and the version (ssm-parameters.md 6.1, 12.4 [M][D]). A
// version that no longer exists is an error, never a fallback (6.1 [D]).
func TestHundredVersionCap(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/cap/labeled", "v1")
		h.put("/cap/free", "v1")
		for i := 2; i <= 100; i++ {
			h.overwrite("/cap/labeled", "v")
			h.overwrite("/cap/free", "v")
		}
		must(h, ssmapi.OpLabelParameterVersion, &ssmapi.LabelParameterVersionInput{Name: "/cap/labeled", ParameterVersion: new(int64(1)), Labels: []string{"pinned"}})
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: "/cap/labeled", Value: "v101", Overwrite: new(true)})
		wantError(h.T, "the 101st version with the oldest labeled", err, ssmapi.IdentityParameterMaxVersionLimitExceeded(),
			"You attempted to create a new version of /cap/labeled by calling the PutParameter API with the overwrite flag. Version 1, the oldest version, can't be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.")
		if got := h.mustGet("/cap/labeled", false); got.Version != 100 {
			h.Errorf("after the refusal Version = %d, want 100", got.Version)
		}

		h.overwrite("/cap/free", "v101")
		hist := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/cap/free", MaxResults: new(int32(50))})
		count := len(hist.Parameters)
		for next := hist.NextToken; next != nil; {
			page := must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: "/cap/free", MaxResults: new(int32(50)), NextToken: next})
			count += len(page.Parameters)
			next = page.NextToken
		}
		if count != 100 {
			h.Errorf("history holds %d versions after the 101st write, want 100", count)
		}
		err = h.get("/cap/free:1")
		wantCode(h.T, "GetParameter of the dropped version 1", err, ssmapi.IdentityParameterVersionNotFound())
		if got := h.mustGet("/cap/free", false); val(got.Value) != "v101" {
			h.Errorf("latest value = %q, want v101", val(got.Value))
		}
	})
}
