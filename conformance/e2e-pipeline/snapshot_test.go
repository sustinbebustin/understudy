package e2e_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/sustinbebustin/understudy/admin"
)

// TestSnapshotResetRestoreKeepsThePipeline saves a snapshot of the running
// pipeline, resets understudy, checks every resource is gone, loads the
// snapshot, checks the state reads back as it was, and publishes again to
// show the restored pipeline still runs end to end.
func TestSnapshotResetRestoreKeepsThePipeline(t *testing.T) {
	p := pipe(t)
	t.Cleanup(func() {
		if t.Failed() {
			reprovision(t)
		}
	})
	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)
	settle(t)
	before := pipelineState(t, p, id)

	var archive bytes.Buffer
	if err := env.admin.SaveSnapshot(t.Context(), &archive, admin.SaveOptions{}); err != nil {
		t.Fatalf("admin snapshot save: %v", err)
	}
	if _, err := env.admin.Reset(t.Context(), admin.ResetRequest{}); err != nil {
		t.Fatalf("admin reset: %v", err)
	}
	assertPipelineGone(t, p, id)

	if _, err := env.admin.LoadSnapshot(t.Context(), bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatalf("admin snapshot load: %v", err)
	}
	settle(t)
	after := pipelineState(t, p, id)
	for _, k := range slices.Sorted(maps.Keys(before)) {
		if before[k] != after[k] {
			t.Errorf("after restore %s = %q, want %q as at the snapshot", k, after[k], before[k])
		}
	}

	next := newID(t)
	publish(t, message{ID: next}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, next)
}

// reprovision rebuilds the shared pipeline after a failed restore, so the
// tests after this one still run against a whole pipeline.
func reprovision(t *testing.T) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	if _, err := env.admin.Reset(ctx, admin.ResetRequest{}); err != nil {
		env.pipe, env.pipeErr = nil, fmt.Errorf("the snapshot test failed and the reset before reprovisioning failed too: %w", err)
		return
	}
	env.pipe, env.pipeErr = provision(ctx, env, env.artifact)
	if env.pipeErr != nil {
		env.pipeErr = fmt.Errorf("the snapshot test failed and reprovisioning failed: %w", env.pipeErr)
	}
}

// pipelineState reads what the pipeline holds into named values, so two
// reads can be compared member by member.
func pipelineState(t *testing.T, p *pipeline, id string) map[string]string {
	t.Helper()
	ctx := t.Context()
	st := map[string]string{}
	fail := func(what string, err error) { t.Fatalf("reading %s: %v", what, err) }

	for _, sp := range specs {
		w := p.workers[sp.name]
		qa, err := env.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(w.queue.url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameAll}})
		if err != nil {
			fail("queue "+w.queue.url, err)
		}
		for _, k := range []string{"QueueArn", "VisibilityTimeout", "RedrivePolicy", "Policy"} {
			st["sqs "+sp.name+" "+k] = qa.Attributes[k]
		}
		sa, err := env.sns.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{SubscriptionArn: aws.String(w.subARN)})
		if err != nil {
			fail("subscription "+w.subARN, err)
		}
		for _, k := range []string{"Endpoint", "FilterPolicy", "FilterPolicyScope", "RawMessageDelivery"} {
			st["sns "+sp.name+" "+k] = sa.Attributes[k]
		}
		fn, err := env.lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(sp.name)})
		if err != nil {
			fail("function "+sp.name, err)
		}
		st["lambda "+sp.name+" CodeSha256"] = aws.ToString(fn.Configuration.CodeSha256)
		st["lambda "+sp.name+" State"] = string(fn.Configuration.State)
		esm, err := env.lambda.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(w.esmUUID)})
		if err != nil {
			fail("mapping "+w.esmUUID, err)
		}
		st["esm "+sp.name] = fmt.Sprintf("%s batch=%d window=%d types=%v", aws.ToString(esm.State), aws.ToInt32(esm.BatchSize), aws.ToInt32(esm.MaximumBatchingWindowInSeconds), esm.FunctionResponseTypes)
	}
	tap, err := env.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: aws.String(p.tap.url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages}})
	if err != nil {
		fail("the tap queue", err)
	}
	st["sqs tap ApproximateNumberOfMessages"] = tap.Attributes["ApproximateNumberOfMessages"]

	sec, err := env.secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	if err != nil {
		fail("the secret", err)
	}
	st["secret value"] = aws.ToString(sec.SecretString)
	st["secret version"] = aws.ToString(sec.VersionId)

	key, err := env.kms.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyAlias)})
	if err != nil {
		fail("the key alias", err)
	}
	st["kms alias target"] = aws.ToString(key.KeyMetadata.Arn)
	st["kms key state"] = string(key.KeyMetadata.KeyState)

	obj, ok := getObject(t, processedKey(goWorker, id))
	if !ok {
		t.Fatalf("the processed object of %s is missing", id)
	}
	st["s3 object"] = string(obj)
	head, err := env.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucketName), Key: aws.String(processedKey(goWorker, id))})
	if err != nil {
		fail("the processed object", err)
	}
	st["s3 object encryption"] = fmt.Sprintf("%s %s", head.ServerSideEncryption, aws.ToString(head.SSEKMSKeyId))

	item, ok := lockItem(t, id)
	if !ok {
		t.Fatalf("the lock of %s is missing", id)
	}
	st["dynamodb lock"] = fmt.Sprintf("%s %s %s", stringAttr(item, "Owner"), stringAttr(item, "MessageId"), stringAttr(item, "ExpiresAt"))
	ttl, err := env.ddb.DescribeTimeToLive(ctx, &dynamodb.DescribeTimeToLiveInput{TableName: aws.String(lockTable)})
	if err != nil {
		fail("the table TTL", err)
	}
	st["dynamodb ttl"] = fmt.Sprintf("%s %s", ttl.TimeToLiveDescription.TimeToLiveStatus, aws.ToString(ttl.TimeToLiveDescription.AttributeName))

	logs, err := env.logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(traceLogGroup), LogStreamName: aws.String("trace-" + id), StartFromHead: aws.Bool(true)})
	if err != nil {
		fail("the trace log stream", err)
	}
	for i, e := range logs.Events {
		st[fmt.Sprintf("logs trace event %d", i)] = aws.ToString(e.Message)
	}

	url, err := env.lambda.GetFunctionUrlConfig(ctx, &lambda.GetFunctionUrlConfigInput{FunctionName: aws.String(imageWorker)})
	if err != nil {
		fail("the function URL", err)
	}
	st["lambda function url"] = aws.ToString(url.FunctionUrl)
	return st
}

// assertPipelineGone checks a reset left none of the pipeline behind:
// each read answers the service's own not-found error.
func assertPipelineGone(t *testing.T, p *pipeline, id string) {
	t.Helper()
	ctx := t.Context()
	gone := func(what string, err error, isNotFound bool) {
		if err == nil {
			t.Errorf("after reset %s still exists", what)
		} else if !isNotFound {
			t.Errorf("after reset reading %s failed with %v, want the service's not-found error", what, err)
		}
	}
	_, err := env.sqs.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String("e2e-" + goWorker)})
	gone("queue e2e-"+goWorker, err, is[*sqstypes.QueueDoesNotExist](err))
	_, err = env.sns.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(p.topicARN)})
	gone("topic "+p.topicARN, err, is[*snstypes.NotFoundException](err))
	_, err = env.lambda.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(goWorker)})
	gone("function "+goWorker, err, is[*lambdatypes.ResourceNotFoundException](err))
	_, err = env.lambda.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(p.workers[goWorker].esmUUID)})
	gone("mapping "+p.workers[goWorker].esmUUID, err, is[*lambdatypes.ResourceNotFoundException](err))
	_, err = env.secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretName)})
	gone("secret "+secretName, err, is[*smtypes.ResourceNotFoundException](err))
	_, err = env.kms.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyAlias)})
	gone("key alias "+keyAlias, err, is[*kmstypes.NotFoundException](err))
	_, err = env.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucketName)})
	gone("bucket "+bucketName, err, is[*s3types.NotFound](err))
	_, err = env.ddb.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(lockTable)})
	gone("table "+lockTable, err, is[*ddbtypes.ResourceNotFoundException](err))
	_, err = env.logs.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(traceLogGroup), LogStreamName: aws.String("trace-" + id)})
	gone("log group "+traceLogGroup, err, err != nil && errorCode(err) == "ResourceNotFoundException")
}

func is[T error](err error) bool {
	_, ok := errors.AsType[T](err)
	return ok
}

func errorCode(err error) string {
	code, _, _ := awsError(err)
	return code
}
