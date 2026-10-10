package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func services() []*registry.Service {
	return []*registry.Service{
		cloudwatchapi.Service(), dynamodbapi.Service(), eventbridgeapi.Service(), iamapi.Service(),
		kmsapi.Service(), lambdaapi.Service(), logsapi.Service(), s3api.Service(),
		secretsmanagerapi.Service(), snsapi.Service(), sqsapi.Service(), stsapi.Service(),
	}
}

// TestTheTwelveServicesRegisterTogether is the check the server runs at
// boot: unique ids, ordinals by name, JSON target and Query (Action,
// Version) tables without collisions, valid route tables.
func TestTheTwelveServicesRegisterTogether(t *testing.T) {
	if _, err := registry.New(services()...); err != nil {
		t.Fatal(err)
	}
}

// TestEveryModeledOperationNamesItsIAMActions: Operation.IAM comes from the
// Service Reference table under the service's IAM prefix, the same-name or
// mapped action first (smithy-models.md 8).
func TestEveryModeledOperationNamesItsIAMActions(t *testing.T) {
	primary := map[string]string{
		"sqs.SendMessageBatch":             "sqs:SendMessage",
		"sqs.DeleteMessageBatch":           "sqs:DeleteMessage",
		"sqs.ChangeMessageVisibilityBatch": "sqs:ChangeMessageVisibility",
		"sns.PublishBatch":                 "sns:Publish",
		"lambda.Invoke":                    "lambda:InvokeFunction",
		"s3.HeadBucket":                    "s3:ListBucket",
		"s3.GetObject":                     "s3:GetObject",
		"s3.HeadObject":                    "s3:GetObject",
		"s3.ListObjects":                   "s3:ListBucket",
		"s3.ListObjectsV2":                 "s3:ListBucket",
		"s3.DeleteObjects":                 "s3:DeleteObject",
		"s3.CopyObject":                    "s3:PutObject",
		"s3.PutBucketReplication":          "s3:PutReplicationConfiguration",
		"dynamodb.TransactWriteItems":      "dynamodb:PutItem",
		"kms.ReEncrypt":                    "kms:ReEncryptFrom",
		"cloudwatch.PutMetricAlarm":        "cloudwatch:PutMetricAlarm",
		"logs.PutLogEvents":                "logs:PutLogEvents",
		"eventbridge.PutRule":              "events:PutRule",
		"dynamodb.BatchWriteItem":          "dynamodb:BatchWriteItem",
		"sts.AssumeRole":                   "sts:AssumeRole",
		// The same-name action is listed under the service's Actions[]
		// only; the operation's authorized list leaves it out.
		"dynamodb.RestoreTableToPointInTime": "dynamodb:RestoreTableToPointInTime",
		"s3.GetObjectAttributes":             "s3:GetObjectAttributes",
		"sts.AssumeRoleWithWebIdentity":      "sts:AssumeRoleWithWebIdentity",
		"logs.StartLiveTail":                 "logs:StartLiveTail",
	}
	for _, svc := range services() {
		for _, op := range svc.Operations {
			if op.Kind != registry.Modeled {
				continue
			}
			for _, a := range op.IAM {
				if a.Prefix == "" || a.Name == "" {
					t.Errorf("%s: IAM action %+v", op.ID, a)
				}
			}
			want, ok := primary[op.ID.String()]
			if !ok {
				continue
			}
			delete(primary, op.ID.String())
			if len(op.IAM) == 0 || op.IAM[0].String() != want {
				t.Errorf("%s: IAM = %v, want %s first", op.ID, op.IAM, want)
			}
		}
	}
	if len(primary) > 0 {
		t.Errorf("operations %v are not in the descriptors", primary)
	}
	if cw := cloudwatchapi.Service(); cw.IAMPrefix != "cloudwatch" || cw.SigningName != "monitoring" {
		t.Errorf("CloudWatch IAM prefix, signing name = %q, %q; want cloudwatch, monitoring", cw.IAMPrefix, cw.SigningName)
	}
}

// TestEveryRouteRowRoundTrips: the generator orders and checks route rows
// with its own copy of the registry's specificity rules (internal/codegen
// routes.go), so each row's minimal request (labels filled, its query
// conditions and required headers present, nothing else) must route back
// to that row's operation through registry.RouteTable.Match.
func TestEveryRouteRowRoundTrips(t *testing.T) {
	for _, svc := range services() {
		if svc.Routes == nil {
			continue
		}
		for i, r := range svc.Routes.Routes {
			segs := make([]string, 0, len(r.Path))
			for j, s := range r.Path {
				switch s := s.(type) {
				case registry.Literal:
					segs = append(segs, url.PathEscape(s.Value))
				case registry.Label:
					segs = append(segs, fmt.Sprintf("label%d", j))
				case registry.Greedy:
					segs = append(segs, fmt.Sprintf("greedy%d/key", j))
				}
			}
			query := url.Values{}
			for _, c := range r.Query {
				switch c := c.(type) {
				case registry.QueryPresent:
					query.Set(c.Key, "")
				case registry.QueryEquals:
					query.Set(c.Key, c.Value)
				}
			}
			header := http.Header{}
			for _, h := range r.Headers {
				header.Set(h, "x")
			}
			path := "/" + strings.Join(segs, "/")
			got, ok := svc.Routes.Match(r.Method, path, query, header)
			if !ok || got.Operation != r.Operation {
				name := "nothing"
				if ok {
					name = got.Operation.ID.String()
				}
				t.Errorf("%s row %d (%s): %s %s?%s routes to %s", svc.ID, i, r.Operation.ID, r.Method, path, query.Encode(), name)
			}
		}
	}
}

// TestRawOperations: the rawOperations of codegen/services are Raw
// descriptors with their IAM actions and no schema (ARCHITECTURE.md 5.3).
func TestRawOperations(t *testing.T) {
	want := map[ref.OperationID][]string{
		{Service: "lambda", Name: "InvokeFunctionUrl"}:    {"lambda:InvokeFunctionUrl"},
		{Service: "sns", Name: "ConfirmSubscriptionLink"}: {"sns:ConfirmSubscription"},
		{Service: "sns", Name: "SigningCert"}:             nil,
		{Service: "sns", Name: "UnsubscribeLink"}:         {"sns:Unsubscribe"},
	}
	got := map[ref.OperationID][]string{}
	for _, svc := range services() {
		for _, op := range svc.Operations {
			if op.Kind != registry.Raw {
				continue
			}
			if op.Input != nil || op.Output != nil {
				t.Errorf("%s: a raw operation has a schema", op.ID)
			}
			var acts []string
			if len(op.IAM) > 0 {
				acts = make([]string, 0, len(op.IAM))
			}
			for _, a := range op.IAM {
				acts = append(acts, a.String())
			}
			got[op.ID] = acts
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw operations = %v, want %v", got, want)
	}
}

// TestDynamoDBBindsEveryOperationRaw: rawBinding gives each modeled
// DynamoDB operation a <Op>Raw binder method for the passthrough (ADR 0014).
func TestDynamoDBBindsEveryOperationRaw(t *testing.T) {
	binder := reflect.TypeFor[dynamodbapi.Binder]()
	for _, op := range dynamodbapi.Service().Operations {
		for _, name := range []string{op.ID.Name, op.ID.Name + "Raw"} {
			if _, ok := binder.MethodByName(name); !ok {
				t.Errorf("dynamodbapi.Binder has no %s", name)
			}
		}
	}
	if _, ok := reflect.TypeFor[sqsapi.Binder]().MethodByName("SendMessageRaw"); ok {
		t.Error("sqsapi.Binder has a Raw method without rawBinding")
	}
}

// TestServiceWideOverlayTraits: the identities the service overlays and
// overlays/common.json give every service (error-matrix.md 3, 5, 7).
func TestServiceWideOverlayTraits(t *testing.T) {
	type unknownFn func(awserr.Family) (awserr.Identity, bool)
	tests := []struct {
		svc       *registry.Service
		throttle  awserr.Identity
		unknown   unknownFn
		family    awserr.Family
		unknownID string
		malformed string
	}{
		{sqsapi.Service(), sqsapi.ThrottleError(), sqsapi.UnknownOperationError, awserr.QueryFamily, "com.amazon.coral.service#InvalidAction", "com.amazon.coral.service#SerializationException"},
		{snsapi.Service(), snsapi.ThrottleError(), snsapi.UnknownOperationError, awserr.QueryFamily, "com.amazon.coral.service#InvalidAction", ""},
		{dynamodbapi.Service(), dynamodbapi.ThrottleError(), dynamodbapi.UnknownOperationError, awserr.JSONFamily, "com.amazon.coral.service#UnknownOperationException", "com.amazon.coral.service#SerializationException"},
		{lambdaapi.Service(), lambdaapi.ThrottleError(), lambdaapi.UnknownOperationError, awserr.JSONFamily, "com.amazonaws.lambda#UnknownOperationException", "com.amazonaws.lambda#InvalidRequestContentException"},
		{logsapi.Service(), logsapi.ThrottleError(), logsapi.UnknownOperationError, awserr.JSONFamily, "com.amazon.coral.service#UnknownOperationException", "com.amazon.coral.service#SerializationException"},
		{secretsmanagerapi.Service(), secretsmanagerapi.ThrottleError(), secretsmanagerapi.UnknownOperationError, awserr.JSONFamily, "com.amazon.coral.service#UnknownOperationException", "com.amazon.coral.service#SerializationException"},
		{kmsapi.Service(), kmsapi.ThrottleError(), kmsapi.UnknownOperationError, awserr.JSONFamily, "com.amazon.coral.service#UnknownOperationException", "com.amazon.coral.service#SerializationException"},
		{iamapi.Service(), iamapi.ThrottleError(), iamapi.UnknownOperationError, awserr.QueryFamily, "com.amazon.coral.service#InvalidAction", ""},
		{stsapi.Service(), stsapi.ThrottleError(), stsapi.UnknownOperationError, awserr.QueryFamily, "com.amazon.coral.service#InvalidAction", ""},
		{eventbridgeapi.Service(), eventbridgeapi.ThrottleError(), eventbridgeapi.UnknownOperationError, awserr.JSONFamily, "com.amazon.coral.service#UnknownOperationException", "com.amazon.coral.service#SerializationException"},
		{cloudwatchapi.Service(), cloudwatchapi.ThrottleError(), cloudwatchapi.UnknownOperationError, awserr.QueryFamily, "com.amazon.coral.service#InvalidAction", "com.amazon.coral.service#SerializationException"},
	}
	for _, tt := range tests {
		if !tt.throttle.Retryable || tt.throttle.Shape == "" {
			t.Errorf("%s: throttle identity %+v is not a retryable modeled or generic shape", tt.svc.ID, tt.throttle)
		}
		id, ok := tt.unknown(tt.family)
		if !ok || string(id.Shape) != tt.unknownID {
			t.Errorf("%s: unknown operation over %v = %s, %v; want %s", tt.svc.ID, tt.family, id.Shape, ok, tt.unknownID)
		}
		if got := string(tt.svc.MalformedRequestError.Shape); got != tt.malformed {
			t.Errorf("%s: malformed request = %q, want %q", tt.svc.ID, got, tt.malformed)
		}
	}
	if !s3api.ThrottleError().Retryable || s3api.ThrottleError().Status != 503 {
		t.Errorf("S3 throttle = %+v, want SlowDown 503", s3api.ThrottleError())
	}
}

// TestGenericVocabularyMatchesTheCatalogue: overlays/common.json and the
// awserr catalogue describe the same protocol-level errors per family; a
// service speaks the vocabulary of each family it belongs to and no other.
// The __type is compared by name alone, since awsJson1_1 and restJson1
// services send it bare.
func TestGenericVocabularyMatchesTheCatalogue(t *testing.T) {
	generic := map[ref.ServiceID]func(awserr.Family) []awserr.Identity{
		"cloudwatch": cloudwatchapi.GenericErrors, "dynamodb": dynamodbapi.GenericErrors, "eventbridge": eventbridgeapi.GenericErrors,
		"iam": iamapi.GenericErrors, "kms": kmsapi.GenericErrors, "lambda": lambdaapi.GenericErrors, "logs": logsapi.GenericErrors,
		"secretsmanager": secretsmanagerapi.GenericErrors, "sns": snsapi.GenericErrors, "sqs": sqsapi.GenericErrors, "sts": stsapi.GenericErrors,
	}
	byShape := func(ids []awserr.Identity) map[string]awserr.Identity {
		out := make(map[string]awserr.Identity, len(ids))
		for _, id := range ids {
			_, name, found := strings.Cut(id.JSONType, "#")
			if found {
				id.JSONType = name
			}
			out[string(id.Shape)] = id
		}
		return out
	}
	for _, svc := range services() {
		speaks := map[awserr.Family]bool{}
		for _, p := range svc.Protocols {
			if f, ok := awserr.FamilyOf(p); ok {
				speaks[f] = true
			}
		}
		fn, ok := generic[svc.ID]
		if !ok {
			if len(speaks) > 0 {
				t.Errorf("%s speaks families %v but is not in this test", svc.ID, speaks)
			}
			continue
		}
		for _, f := range []awserr.Family{awserr.JSONFamily, awserr.QueryFamily} {
			var want []awserr.Identity
			if speaks[f] {
				want = awserr.Catalogue(f)
			}
			if got := byShape(fn(f)); !reflect.DeepEqual(got, byShape(want)) {
				t.Errorf("%s: GenericErrors(%v) =\n%+v\nwant\n%+v", svc.ID, f, got, byShape(want))
			}
		}
	}
}

// TestKMSStatusOverrides: KMS documents 400, 400, 400 and 500 where the
// model says 404, 409, 409 and 503 (iam-sts-kms.md Implications 3).
func TestKMSStatusOverrides(t *testing.T) {
	for id, want := range map[awserr.Identity]int{
		kmsapi.IdentityNotFoundException():          400,
		kmsapi.IdentityDisabledException():          400,
		kmsapi.IdentityKMSInvalidStateException():   400,
		kmsapi.IdentityDependencyTimeoutException(): 500,
	} {
		if id.Status != want || id.QueryModeStatus != 0 {
			t.Errorf("%s: status %d, query-mode status %d; want %d and none", id.Shape, id.Status, id.QueryModeStatus, want)
		}
	}
}

// TestCloudWatchResourceNotFoundStatus: 400 over JSON and CBOR, 404 over
// Query and under x-amzn-query-mode (error-matrix.md 5.13).
func TestCloudWatchResourceNotFoundStatus(t *testing.T) {
	if id := cloudwatchapi.IdentityResourceNotFound(); id.Status != 400 || id.QueryModeStatus != 404 {
		t.Fatalf("ResourceNotFound = %+v", id)
	}
}

// TestS3UnmodeledErrors: the twenty S3 codes the model lacks are generated
// identities with S3's <Message> (wire-rest.md 2.4, smithy-models.md 7).
func TestS3UnmodeledErrors(t *testing.T) {
	want := map[awserr.Identity]int{
		s3api.IdentityNoSuchBucketPolicy():                             404,
		s3api.IdentityNoSuchLifecycleConfiguration():                   404,
		s3api.IdentityNoSuchCORSConfiguration():                        404,
		s3api.IdentityNoSuchTagSet():                                   404,
		s3api.IdentityNoSuchWebsiteConfiguration():                     404,
		s3api.IdentityServerSideEncryptionConfigurationNotFoundError(): 404,
		s3api.IdentityReplicationConfigurationNotFoundError():          404,
		s3api.IdentityOwnershipControlsNotFoundError():                 404,
		s3api.IdentityNoSuchPublicAccessBlockConfiguration():           404,
		s3api.IdentityObjectLockConfigurationNotFoundError():           404,
		s3api.IdentityPreconditionFailed():                             412,
		s3api.IdentityNotModified():                                    304,
		s3api.IdentityInvalidRange():                                   416,
		s3api.IdentityBucketNotEmpty():                                 409,
		s3api.IdentityEntityTooSmall():                                 400,
		s3api.IdentityInvalidPart():                                    400,
		s3api.IdentityBadDigest():                                      400,
		s3api.IdentityMalformedXML():                                   400,
		s3api.IdentitySignatureDoesNotMatch():                          403,
		s3api.IdentityNoSuchVersion():                                  404,
	}
	for id, status := range want {
		if code := strings.TrimPrefix(string(id.Shape), "com.amazonaws.s3#"); id.QueryCode != code || id.Status != status || id.MessageKey != awserr.MessageUpper {
			t.Errorf("%s: code %s, status %d, message key %v; want %s, %d and <Message>", id.Shape, id.QueryCode, id.Status, id.MessageKey, code, status)
		}
	}
	if len(want) != 20 {
		t.Fatalf("%d distinct identities, want the twenty codes", len(want))
	}
}
