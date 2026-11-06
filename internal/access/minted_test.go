package access_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The Service Authorization Reference resource-type tables each row of
// TestMintedPatternRendersEachResourceType cites for the resource ARN
// format (aws-doc).
const (
	sarKMS     = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html"
	sarSecrets = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_secretsmanager.html" //nolint:gosec // G101: a documentation URL, not a credential
	sarSNS     = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html"
	sarLambda  = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_lambda.html"
	sarLogs    = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html"
	sarS3      = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_s3.html"
	sarSQS     = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_sqs.html"
	sarIAM     = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_iam.html"
	sarDynamo  = "https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html"
)

// A segment understudy draws renders as the narrowest pattern matching
// every AWS-form id of its resource type, keeping partition, region and
// account (ARCHITECTURE.md 17): one row per resource type, then the
// ARNs that stay exact.
func TestMintedPatternRendersEachResourceType(t *testing.T) {
	const (
		secretARN  = "arn:aws:secretsmanager:us-east-1:111111111111:secret:prod/db-a1B2c3" //nolint:gosec // G101: an ARN, not a credential
		subARN     = topicARN + ":0f1e2d3c-4b5a-4968-8778-695a4b3c2d1e"
		mappingARN = "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:6b2f1c3a-1d2e-4f5a-8b6c-7d8e9f0a1b2c"
		// lambdaStream is the form Lambda names an execution
		// environment's stream (internal/service/lambda/destination/
		// testdata/success-sqs.json, log_stream_name).
		lambdaStream = groupARN + ":log-stream:2026/10/26/[$LATEST]0123456789abcdef0123456789abcdef"
	)
	tests := []struct {
		name    string
		source  string
		mode    access.Mode
		arn     aws.ARN
		pattern string // "" when not minted
	}{
		// arn:${Partition}:kms:${Region}:${Account}:key/${KeyId}
		{"kms key, direct", sarKMS, access.Direct{}, arn(t, keyARN), "arn:aws:kms:us-east-1:111111111111:key/*"},
		{"kms key, as caller", sarKMS, access.AsCaller{}, arn(t, keyARN), "arn:aws:kms:us-east-1:111111111111:key/*"},
		{"kms key, as execution role", sarKMS, access.AsExecutionRole{}, arn(t, keyARN), "arn:aws:kms:us-east-1:111111111111:key/*"},
		{
			"kms key, other partition", sarKMS,
			access.Direct{},
			arn(t, "arn:aws-us-gov:kms:us-gov-west-1:222222222222:key/mrk-1234abcd12ab34cd56ef1234567890ab"),
			"arn:aws-us-gov:kms:us-gov-west-1:222222222222:key/*",
		},
		{"kms alias", sarKMS, access.Direct{}, arn(t, "arn:aws:kms:us-east-1:111111111111:alias/app"), ""},
		{"kms key without an id", sarKMS, access.Direct{}, aws.ARN{Partition: "aws", Service: "kms", Region: "us-east-1", Account: "111111111111", Resource: "key/"}, ""},

		// arn:${Partition}:secretsmanager:${Region}:${Account}:secret:${SecretId};
		// the id is the name, a hyphen and six random characters
		// (secrets-logs-events-alarms.md 2.1).
		{"secret with its suffix", sarSecrets, access.Direct{}, arn(t, secretARN), "arn:aws:secretsmanager:us-east-1:111111111111:secret:prod/db-??????"},
		{
			"secret whose name holds hyphens", sarSecrets,
			access.AsCaller{},
			arn(t, "arn:aws:secretsmanager:eu-west-1:333333333333:secret:my-app-key-ZMysbk"),
			"arn:aws:secretsmanager:eu-west-1:333333333333:secret:my-app-key-??????",
		},
		{"secret without a suffix", sarSecrets, access.Direct{}, arn(t, "arn:aws:secretsmanager:us-east-1:111111111111:secret:plain"), ""},
		{"secret with a short suffix", sarSecrets, access.Direct{}, arn(t, "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-a1b2c"), ""},
		{"secret with a long suffix", sarSecrets, access.Direct{}, arn(t, "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-a1b2c3d"), ""},
		{"secret suffix with a non-alphanumeric", sarSecrets, access.Direct{}, arn(t, "arn:aws:secretsmanager:us-east-1:111111111111:secret:db-a1b2_3"), ""},
		{"secret with an empty name", sarSecrets, access.Direct{}, arn(t, "arn:aws:secretsmanager:us-east-1:111111111111:secret:-a1b2c3"), ""},

		// A subscription ARN is its topic ARN, a colon and the
		// subscription id.
		{"sns subscription", sarSNS, access.Direct{}, arn(t, subARN), topicARN + ":*"},
		{"sns topic", sarSNS, access.Direct{}, arn(t, topicARN), ""},
		{"sns resource with two colons", sarSNS, access.Direct{}, arn(t, subARN+":extra"), ""},

		// arn:${Partition}:lambda:${Region}:${Account}:event-source-mapping:${UUID}
		{"lambda event source mapping", sarLambda, access.AsExecutionRole{}, arn(t, mappingARN), "arn:aws:lambda:us-east-1:111111111111:event-source-mapping:*"},
		{"lambda function", sarLambda, access.Direct{}, arn(t, "arn:aws:lambda:us-east-1:111111111111:function:worker"), ""},

		// arn:${Partition}:logs:${Region}:${Account}:log-group:${LogGroupName}:log-stream:${LogStreamName};
		// only the stream Lambda creates for an execution environment is
		// minted, so only under AsExecutionRole.
		{"lambda's log stream, as execution role", sarLogs, access.AsExecutionRole{}, arn(t, lambdaStream), groupARN + ":log-stream:*"},
		{"log stream, direct", sarLogs, access.Direct{}, arn(t, lambdaStream), ""},
		{"log stream, as caller", sarLogs, access.AsCaller{}, arn(t, lambdaStream), ""},
		{"log stream, no mode", sarLogs, nil, arn(t, lambdaStream), ""},
		{"log group, as execution role", sarLogs, access.AsExecutionRole{}, arn(t, groupARN), ""},
		{
			// The caller-named group is escaped as an exact ARN is
			// (policydoc.Literal); the added * is not.
			"log stream of a group holding a wildcard character", sarLogs,
			access.AsExecutionRole{},
			arn(t, "arn:aws:logs:us-east-1:111111111111:log-group:run?1:log-stream:abc"),
			"arn:aws:logs:us-east-1:111111111111:log-group:run${?}1:log-stream:*",
		},

		{"s3 bucket", sarS3, access.Direct{}, arn(t, "arn:aws:s3:::uploads"), ""},
		{"s3 object", sarS3, access.Direct{}, arn(t, objectARN), ""},
		{"sqs queue", sarSQS, access.AsExecutionRole{}, arn(t, queueARN), ""},
		{"iam role", sarIAM, access.Direct{}, arn(t, roleARN), ""},
		{"dynamodb table", sarDynamo, access.Direct{}, arn(t, "arn:aws:dynamodb:us-east-1:111111111111:table/orders"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pattern, minted := access.MintedPattern(tc.mode, tc.arn)
			if want := tc.pattern != ""; minted != want || pattern != tc.pattern {
				t.Errorf("MintedPattern(%T, %s) = %q, %t; want %q, %t (%s)", tc.mode, tc.arn, pattern, minted, tc.pattern, want, tc.source)
			}
		})
	}
}

// mintedKind draws one minted resource type: an ARN of it in a drawn
// partition, region and account, a second id of the same type and scope
// (another key, the secret recreated, another subscription), and the
// mode the use is recorded under.
type mintedKind struct {
	arn, sibling aws.ARN
	mode         access.Mode
	action       string
}

func drawMinted(t *rapid.T) mintedKind {
	partition := rapid.SampledFrom([]string{"aws", "aws-cn", "aws-us-gov"}).Draw(t, "partition")
	region := rapid.SampledFrom([]string{"us-east-1", "eu-west-1", "ap-southeast-2"}).Draw(t, "region")
	account := rapid.StringMatching(`[0-9]{12}`).Draw(t, "account")
	at := func(service, resource string) aws.ARN {
		return aws.ARN{Partition: partition, Service: service, Region: region, Account: account, Resource: resource}
	}
	uuid := func(label string) string {
		return rapid.StringMatching(`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).Draw(t, label)
	}
	// Secret names: letters, digits and /_+=.@- (secrets-logs-events-alarms.md 2.1).
	suffix := func(label string) string { return rapid.StringMatching(`[A-Za-z0-9]{6}`).Draw(t, label) }
	switch rapid.IntRange(0, 4).Draw(t, "kind") {
	case 0:
		return mintedKind{at("kms", "key/"+uuid("key")), at("kms", "key/"+uuid("other key")), access.AsCaller{}, "kms:Decrypt"}
	case 1:
		name := rapid.StringMatching(`[A-Za-z0-9/_+=.@-]{1,20}`).Draw(t, "secret")
		return mintedKind{at("secretsmanager", "secret:"+name+"-"+suffix("suffix")), at("secretsmanager", "secret:"+name+"-"+suffix("other suffix")), access.Direct{}, "secretsmanager:GetSecretValue"}
	case 2:
		topic := rapid.StringMatching(`[A-Za-z0-9_-]{1,20}`).Draw(t, "topic")
		return mintedKind{at("sns", topic+":"+uuid("subscription")), at("sns", topic+":"+uuid("other subscription")), access.Direct{}, "sns:Unsubscribe"}
	case 3:
		return mintedKind{at("lambda", "event-source-mapping:"+uuid("mapping")), at("lambda", "event-source-mapping:"+uuid("other mapping")), access.Direct{}, "lambda:DeleteEventSourceMapping"}
	default:
		group := rapid.StringMatching(`/aws/lambda/[A-Za-z0-9_-]{1,20}`).Draw(t, "group")
		stream := func(label string) string {
			return rapid.StringMatching(`20[0-9]{2}/[01][0-9]/[0-3][0-9]/\[\$LATEST\][0-9a-f]{32}`).Draw(t, label)
		}
		return mintedKind{at("logs", "log-group:"+group+":log-stream:"+stream("stream")), at("logs", "log-group:"+group+":log-stream:"+stream("other stream")), access.AsExecutionRole{}, "logs:PutLogEvents"}
	}
}

// A minted pattern allows its own ARN and every other id of the same type
// in the same scope, keeps the ARN's partition, region and account, and
// allows no ARN of another account: the narrowest pattern that matches
// every AWS-form id (ARCHITECTURE.md 17).
func TestMintedPatternMatchesEveryIDOfItsTypeProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := drawMinted(t)
		pattern, minted := access.MintedPattern(k.mode, k.arn)
		if !minted {
			t.Fatalf("MintedPattern(%T, %s) reports not minted", k.mode, k.arn)
		}
		scope := "arn:" + k.arn.Partition + ":" + k.arn.Service + ":" + k.arn.Region + ":" + k.arn.Account + ":"
		if !strings.HasPrefix(pattern, scope) {
			t.Fatalf("MintedPattern(%s) = %q, want it to keep %q", k.arn, pattern, scope)
		}
		doc := policydoc.Document{Version: policydoc.Version20121017, Statements: []policydoc.Statement{{
			Effect: policydoc.Allow, Action: policydoc.Action{k.action}, Resource: policydoc.Resource{pattern},
		}}}
		allows := func(a aws.ARN) bool {
			_, ok := policydoc.Allows(doc, policydoc.Query{Action: k.action, Resource: a.String()}).(policydoc.Allowed)
			return ok
		}
		if !allows(k.arn) || !allows(k.sibling) {
			t.Fatalf("pattern %q allows %s: %t, %s: %t; want both", pattern, k.arn, allows(k.arn), k.sibling, allows(k.sibling))
		}
		other := k.arn
		other.Account = "999999999999"
		if other.Account == k.arn.Account {
			other.Account = "888888888888"
		}
		if allows(other) {
			t.Fatalf("pattern %q allows %s in another account", pattern, other)
		}
	})
}
