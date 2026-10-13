package logs_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kms"
)

// withKMS serves KMS beside Logs, which CreateLogGroup resolves kmsKeyId
// through.
func withKMS() kerneltest.Option { return kerneltest.Services(kmsapi.Service()) }

// key creates a KMS key and returns its ARN.
func (h *harness) key() string {
	h.Helper()
	out := must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{})
	if out.KeyMetadata == nil || out.KeyMetadata.Arn == nil {
		h.Fatalf("CreateKey = %+v, want the key's ARN", out)
	}
	return *out.KeyMetadata.Arn
}

// CreateLogGroup takes kmsKeyId as the ARN of an enabled key: it refuses a
// bare key ID, an alias, a missing key and a disabled one with
// InvalidParameterException "The specified KMS Key Id could not be found.",
// as AWS answers Terraform's kms_key_id = aws_kms_key.k.id (third-party:
// hashicorp/terraform-provider-aws#41606), and stores and echoes the ARN of
// an enabled one.
func TestCreateLogGroupChecksTheKMSKey(t *testing.T) {
	run(t, func(h *harness) {
		kms.New(h.Deps(kmsapi.Service()), kms.Options{})
		enabled := h.key()
		must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: "enabled", KmsKeyId: new(enabled)})
		out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{LogGroupNamePrefix: new("enabled")})
		if len(out.LogGroups) != 1 || val(out.LogGroups[0].KmsKeyId) != enabled {
			h.Errorf("DescribeLogGroups = %+v, want kmsKeyId %s", out.LogGroups, enabled)
		}

		bareID := enabled[strings.LastIndex(enabled, "/")+1:]
		disabled := h.key()
		must(h, kmsapi.OpDisableKey, &kmsapi.DisableKeyInput{KeyId: disabled})
		for name, key := range map[string]string{
			"bare-id":   bareID,
			"alias":     "alias/logs",
			"alias-arn": "arn:aws:kms:us-east-1:000000000000:alias/logs",
			"missing":   "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000",
			"disabled":  disabled,
		} {
			_, err := call(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: name, KmsKeyId: new(key)})
			wantError(h.T, err, "InvalidParameterException", "The specified KMS Key Id could not be found.")
		}

		// A refused key creates nothing.
		if out := must(h, logsapi.OpDescribeLogGroups, &logsapi.DescribeLogGroupsInput{}); len(out.LogGroups) != 1 {
			h.Errorf("groups after refused keys = %d, want 1", len(out.LogGroups))
		}
	}, withKMS())
}
