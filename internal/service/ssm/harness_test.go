package ssm_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kms"
	"github.com/sustinbebustin/understudy/internal/service/ssm"
)

// arnPrefix is the parameter ARN prefix in the harness scope, account
// 000000000000 in us-east-1 (ssm-parameters.md 3.3:
// arn:<partition>:ssm:<region>:<account>:parameter/<name without its
// leading slash>).
const arnPrefix = "arn:aws:ssm:us-east-1:000000000000:parameter/"

// harness is a kernel with Parameter Store and the KMS it encrypts through.
type harness struct {
	*kerneltest.K
	svc *ssm.Service
}

func run(t *testing.T, fn func(h *harness), opts ...kerneltest.Option) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		kms.New(k.Deps(kmsapi.Service()), kms.Options{})
		svc := ssm.New(k.Deps(ssmapi.Service()), ssm.Options{})
		fn(&harness{K: k, svc: svc})
	}, append([]kerneltest.Option{kerneltest.Services(kmsapi.Service(), ssmapi.Service())}, opts...)...)
}

func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s (%d): %q", op.Descriptor().ID.Name, err.ID.Shape, err.ID.Status, err.Message)
	}
	return out
}

func errOf[Out any](_ *Out, err *awserr.Error) *awserr.Error { return err }

func val[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// put creates name as a String holding value.
func (h *harness) put(name, value string) *ssmapi.PutParameterOutput {
	h.Helper()
	return must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: value, Type: new(ssmapi.ParameterTypeString)})
}

// overwrite writes value over name with Overwrite: true and nothing else.
func (h *harness) overwrite(name, value string) *ssmapi.PutParameterOutput {
	h.Helper()
	return must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: value, Overwrite: new(true)})
}

func (h *harness) putSecure(name, value string, keyID *string) {
	h.Helper()
	must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: value, Type: new(ssmapi.ParameterTypeSecureString), KeyId: keyID})
}

// get reads name and returns only the error, for the tests of refusals
// and absence.
func (h *harness) get(name string) *awserr.Error {
	return errOf(call(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: name}))
}

func (h *harness) mustGet(name string, decrypt bool) ssmapi.Parameter {
	h.Helper()
	out := must(h, ssmapi.OpGetParameter, &ssmapi.GetParameterInput{Name: name, WithDecryption: new(decrypt)})
	if out.Parameter == nil {
		h.Fatalf("GetParameter %s answered no Parameter", name)
	}
	return *out.Parameter
}

// describeName is Terraform's exact DescribeParameters request
// (ssm-parameters.md 7.5, [T] parameter.go:L490-L529).
func (h *harness) describeName(name string) []ssmapi.ParameterMetadata {
	h.Helper()
	return must(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{
		ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Name", Option: new("Equals"), Values: []string{name}}},
	}).Parameters
}

func (h *harness) mustDescribeOne(name string) ssmapi.ParameterMetadata {
	h.Helper()
	got := h.describeName(name)
	if len(got) != 1 {
		h.Fatalf("DescribeParameters Name Equals %s = %d entries, want 1", name, len(got))
	}
	return got[0]
}

func (h *harness) history(name string) []ssmapi.ParameterHistory {
	h.Helper()
	return must(h, ssmapi.OpGetParameterHistory, &ssmapi.GetParameterHistoryInput{Name: name}).Parameters
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func (h *harness) createKey() string {
	h.Helper()
	return must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{}).KeyMetadata.KeyId
}

// openWithKMS decrypts a SecureString value read without decryption: the
// base64 of the KMS ciphertext blob, under encryption context
// PARAMETER_ARN=<arn> (ssm-parameters.md 5.2, 5.3, aws-doc
// https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html).
func (h *harness) openWithKMS(value, arn string) (string, *awserr.Error) {
	h.Helper()
	blob, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		h.Fatalf("value %q read without decryption is not base64: %v", value, err)
	}
	out, aerr := call(h, kmsapi.OpDecrypt, &kmsapi.DecryptInput{CiphertextBlob: blob, EncryptionContext: map[string]string{"PARAMETER_ARN": arn}})
	if aerr != nil {
		return "", aerr
	}
	return string(out.Plaintext), nil
}

// switchEntry is the Parameter Store fidelity switch name. A test of a
// behavior no AWS evidence fixes reads the value understudy chose from the
// switch rather than repeating it (ARCHITECTURE.md principle 2).
func (h *harness) switchEntry(name string) fidelity.Entry {
	h.Helper()
	for _, e := range h.svc.Switches() {
		if e.Name == name {
			return e
		}
	}
	h.Fatalf("Switches() lists no %s; switches = %+v", name, h.svc.Switches())
	return fidelity.Entry{}
}

// follows fails the test unless switch name is set to value: the
// assertions after it test that value's behavior.
func (h *harness) follows(name, value string) {
	h.Helper()
	if got := h.switchEntry(name).Value; got != value {
		h.Fatalf("switch %s = %q; this test asserts the %q behavior", name, got, value)
	}
}

// wantCode fails unless err has identity id (ssm-parameters.md 2.2: every
// client error is 400, InternalServerError 500).
func wantCode(tb testing.TB, what string, err *awserr.Error, id awserr.Identity) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Errorf("%s: no error, want %s", what, id.Shape)
	case err.ID.Shape != id.Shape || err.ID.Status != id.Status:
		tb.Errorf("%s = %s (%d) %q, want %s (%d)", what, err.ID.Shape, err.ID.Status, err.Message, id.Shape, id.Status)
	}
}

// wantError fails unless err has identity id and exactly message.
func wantError(tb testing.TB, what string, err *awserr.Error, id awserr.Identity, message string) {
	tb.Helper()
	wantCode(tb, what, err, id)
	if err != nil && err.Message != message {
		tb.Errorf("%s message = %q\nwant %q", what, err.Message, message)
	}
}

// wantOwnMessage checks an understudy-owned message by the rule for
// them: it names each of parts (the parameter or offending input and the
// limit) and never holds the request's value.
func wantOwnMessage(tb testing.TB, what string, err *awserr.Error, value string, parts ...string) {
	tb.Helper()
	if err == nil {
		return
	}
	for _, p := range parts {
		if !strings.Contains(err.Message, p) {
			tb.Errorf("%s message %q does not name %q", what, err.Message, p)
		}
	}
	if value != "" && strings.Contains(err.Message, value) {
		tb.Errorf("%s message %q holds the request's value", what, err.Message)
	}
}

// wantNotImplemented fails unless err is the 501 NotImplemented naming
// feature and the issues link.
func wantNotImplemented(tb testing.TB, what string, err *awserr.Error, feature string) {
	tb.Helper()
	wantCode(tb, what, err, ssmapi.IdentityNotImplemented())
	if err == nil {
		return
	}
	for _, p := range []string{feature, "https://github.com/sustinbebustin/understudy/issues"} {
		if !strings.Contains(err.Message, p) {
			tb.Errorf("%s message %q does not name %q", what, err.Message, p)
		}
	}
}

// bubbleTB hands rapid a synctest bubble's test without its Deadline
// method, which panics inside a bubble; rapid only reads it to stop early.
type bubbleTB struct{ testing.TB }

// names lists the Name of each item, in order.
func names[T any](items []T, name func(T) *string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = val(name(it))
	}
	return out
}

func paramName(p ssmapi.Parameter) *string        { return p.Name }
func metaName(p ssmapi.ParameterMetadata) *string { return p.Name }

// versions lists the Version of each history entry, in order.
func versions(ps []ssmapi.ParameterHistory) []int64 {
	out := make([]int64, len(ps))
	for i := range ps {
		out[i] = ps[i].Version
	}
	return out
}
