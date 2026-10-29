package ids_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ids"
)

// Kinds lists every generator kind in value order, KindRequestID first, so
// the web UI's label table, generated from it, names each one
// (ARCHITECTURE.md 17).
func TestKindsListsEveryDeclaredKind(t *testing.T) {
	named, _ := declaredKinds()
	if got := ids.Kinds(); !slices.Equal(got, named) || got[0] != ids.KindRequestID {
		t.Fatalf("Kinds() = %v, want %v", got, named)
	}
}

// kindNames are the stream names of every generator kind: each seeds its
// stream and keys it in snapshot manifests (ADR 0018, ADR 0009), so a
// renamed kind would shift a seeded run's ids and orphan a saved position.
// A kind added later is appended after them.
var kindNames = []struct {
	k    ids.Kind
	name string
}{
	{ids.KindRequestID, "request-id"},
	{ids.KindMessageID, "sqs-message-id"},
	{ids.KindReceiptHandle, "sqs-receipt-handle"},
	{ids.KindSNSMessageID, "sns-message-id"},
	{ids.KindLambdaRequestID, "lambda-request-id"},
	{ids.KindESMID, "lambda-esm-id"},
	{ids.KindUploadID, "s3-upload-id"},
	{ids.KindVersionID, "s3-version-id"},
	{ids.KindETag, "s3-etag"},
	{ids.KindKMSKeyID, "kms-key-id"},
	{ids.KindIAMID, "iam-id"},
	{ids.KindAccessKeyID, "access-key-id"},
	{ids.KindSecretAccessKey, "secret-access-key"},
	{ids.KindSessionToken, "session-token"},
	{ids.KindDeliveryID, "delivery-id"},
	{ids.KindFaultStream, "fault-stream"},
	{ids.KindJitter, "jitter"},
	{ids.KindEventID, "events-event-id"},
	{ids.KindSSECSalt, "s3-ssec-salt"},
	{ids.KindLambdaRecordKey, "lambda-record-key"},
	{ids.KindAPIGatewayAPIID, "apigateway-api-id"},
	{ids.KindAPIGatewayChildID, "apigateway-child-id"},
	{ids.KindStreamEventID, "dynamodb-stream-event-id"},
	{ids.KindStreamIteratorKey, "shard-iterator-key"},
	{ids.KindSFNExecutionName, "sfn-execution-name"},
	{ids.KindSFNRevisionID, "sfn-revision-id"},
	{ids.KindSFNUUID, "sfn-uuid"},
	{ids.KindSFNMathRandom, "sfn-math-random"},
	{ids.KindSFNJitter, "sfn-jitter"},
	{ids.KindSESMessageID, "ses-message-id"},
	{ids.KindSESDomainToken, "ses-domain-token"},
	{ids.KindSESDkimToken, "ses-dkim-token"},
	{ids.KindSESLinkToken, "ses-link-token"},
}

// declaredKinds walks the kinds from the first until String falls back to
// its numeric form.
func declaredKinds() (named []ids.Kind, next ids.Kind) {
	k := ids.KindRequestID
	for ; !strings.HasPrefix(k.String(), "kind("); k++ {
		named = append(named, k)
	}
	return named, k
}

func TestKindNamesAreStable(t *testing.T) {
	named, next := declaredKinds()
	if len(named) < len(kindNames) {
		t.Fatalf("declared kinds %v stop before the persisted ones end", named)
	}
	for i, c := range kindNames {
		if named[i] != c.k {
			t.Errorf("declared kind %d is %d, want %s: persisted kinds keep their place", i, named[i], c.name)
		}
		if got := c.k.String(); got != c.name {
			t.Errorf("Kind %d String() = %q, want %q", c.k, got, c.name)
		}
		if got, err := c.k.MarshalText(); err != nil || string(got) != c.name {
			t.Errorf("Kind %d MarshalText() = %q, %v; want %q", c.k, got, err, c.name)
		}
		var back ids.Kind
		if err := back.UnmarshalText([]byte(c.name)); err != nil || back != c.k {
			t.Errorf("UnmarshalText(%q) = %v, %v; want %v", c.name, back, err, c.k)
		}
	}
	if _, err := next.MarshalText(); err == nil {
		t.Errorf("MarshalText of %v, after the last declared kind, succeeded", next)
	}
}

// The error lists every declared kind; for the persisted set that is the
// text understudy has always written.
func TestUnknownKindErrorListsTheKnownKinds(t *testing.T) {
	var k ids.Kind
	err := k.UnmarshalText([]byte("sqs-message-ids"))
	if _, ok := errors.AsType[*ids.UnknownKindError](err); !ok {
		t.Fatalf("UnmarshalText of an unknown name = %v, want *UnknownKindError", err)
	}
	named, _ := declaredKinds()
	names := make([]string, 0, len(named))
	for _, k := range named {
		names = append(names, k.String())
	}
	want := `ids: unknown generator kind "sqs-message-ids"; known kinds are ` + strings.Join(names, ", ")
	if got := err.Error(); got != want {
		t.Errorf("error = %q\nwant    %q", got, want)
	}
	const persisted = "request-id, sqs-message-id, sqs-receipt-handle, sns-message-id, lambda-request-id, lambda-esm-id, s3-upload-id, s3-version-id, s3-etag, kms-key-id, iam-id, access-key-id, secret-access-key, session-token, delivery-id, fault-stream, jitter, events-event-id, s3-ssec-salt"
	if !strings.Contains(err.Error(), "known kinds are "+persisted) {
		t.Errorf("error = %q, want it to list the persisted kinds in order", err)
	}
}
