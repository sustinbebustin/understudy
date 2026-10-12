// Command goworker is the provided.al2023 worker of the e2e pipeline. It is
// fed by an SQS event source mapping with ReportBatchItemFailures: for each
// record it reads the pipeline secret, takes a DynamoDB lock whose expiry
// is the record's SentTimestamp plus LOCK_TTL_SECONDS, writes the record to
// the SSE-KMS bucket, writes one line to a per-message log stream in
// TRACE_LOG_GROUP and prints a JSON line to its own log. A message whose
// payload says "fail" is reported in batchItemFailures, so SQS redrives it
// to the dead-letter queue after maxReceiveCount.
//
// The binary is built by the suite (GOOS=linux, CGO_ENABLED=0) and shipped
// as the bootstrap of a zip; the AWS SDK finds understudy through the
// AWS_ENDPOINT_URL the emulator sets in the execution environment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// payload is the application message published to the topic.
type payload struct {
	ID   string `json:"id"`
	Fail bool   `json:"fail"`
}

// processed is the object written to the bucket for each handled record.
type processed struct {
	Function        string            `json:"function"`
	SecretVersionID string            `json:"secretVersionId"`
	Record          events.SQSMessage `json:"record"`
	Payload         payload           `json:"payload"`
	Lock            map[string]string `json:"lock"`
}

type worker struct {
	name     string
	secretID string
	table    string
	bucket   string
	logGroup string
	ttl      int64
	secrets  *secretsmanager.Client
	ddb      *dynamodb.Client
	s3       *s3.Client
	logs     *cloudwatchlogs.Client
}

func main() {
	w, err := newWorker(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "goworker: init: %v\n", err)
		os.Exit(1)
	}
	lambda.Start(w.handle)
}

func newWorker(ctx context.Context) (*worker, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading the SDK configuration: %w", err)
	}
	ttl, err := strconv.ParseInt(os.Getenv("LOCK_TTL_SECONDS"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("LOCK_TTL_SECONDS %q is not an integer: %w", os.Getenv("LOCK_TTL_SECONDS"), err)
	}
	return &worker{
		name:     os.Getenv("AWS_LAMBDA_FUNCTION_NAME"),
		secretID: os.Getenv("SECRET_ID"),
		table:    os.Getenv("LOCK_TABLE"),
		bucket:   os.Getenv("BUCKET"),
		logGroup: os.Getenv("TRACE_LOG_GROUP"),
		ttl:      ttl,
		secrets:  secretsmanager.NewFromConfig(cfg),
		ddb:      dynamodb.NewFromConfig(cfg),
		s3:       s3.NewFromConfig(cfg),
		logs:     cloudwatchlogs.NewFromConfig(cfg),
	}, nil
}

func (w *worker) handle(ctx context.Context, ev events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for _, rec := range ev.Records {
		if err := w.process(ctx, rec); err != nil {
			logLine(map[string]string{"event": "failed", "id": idOf(rec), "messageId": rec.MessageId, "function": w.name, "receiveCount": rec.Attributes["ApproximateReceiveCount"], "error": err.Error()})
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
		}
	}
	return resp, nil
}

var errAsked = errors.New("the payload asked for a failure")

func (w *worker) process(ctx context.Context, rec events.SQSMessage) error {
	var p payload
	if err := json.Unmarshal([]byte(rec.Body), &p); err != nil {
		return fmt.Errorf("the body is not a JSON payload: %w", err)
	}
	if p.Fail {
		return errAsked
	}
	secret, err := w.secrets.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(w.secretID)})
	if err != nil {
		return fmt.Errorf("reading the secret: %w", err)
	}
	sentMs, err := strconv.ParseInt(rec.Attributes["SentTimestamp"], 10, 64)
	if err != nil {
		return fmt.Errorf("SentTimestamp %q: %w", rec.Attributes["SentTimestamp"], err)
	}
	now := sentMs / 1000
	lock := map[string]string{"LockID": "lock#" + p.ID, "ExpiresAt": strconv.FormatInt(now+w.ttl, 10), "Owner": w.name, "MessageId": rec.MessageId}
	_, err = w.ddb.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(w.table),
		Item: map[string]ddbtypes.AttributeValue{
			"LockID":    &ddbtypes.AttributeValueMemberS{Value: lock["LockID"]},
			"ExpiresAt": &ddbtypes.AttributeValueMemberN{Value: lock["ExpiresAt"]},
			"Owner":     &ddbtypes.AttributeValueMemberS{Value: w.name},
			"MessageId": &ddbtypes.AttributeValueMemberS{Value: rec.MessageId},
		},
		ConditionExpression:       aws.String("attribute_not_exists(LockID) OR ExpiresAt < :now"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{":now": &ddbtypes.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)}},
	})
	if _, held := errors.AsType[*ddbtypes.ConditionalCheckFailedException](err); held {
		logLine(map[string]string{"event": "locked", "id": p.ID, "messageId": rec.MessageId, "function": w.name})
		return nil
	}
	if err != nil {
		return fmt.Errorf("taking the lock: %w", err)
	}
	body, err := json.Marshal(processed{Function: w.name, SecretVersionID: aws.ToString(secret.VersionId), Record: rec, Payload: p, Lock: lock})
	if err != nil {
		return fmt.Errorf("encoding the record: %w", err)
	}
	key := "processed/" + w.name + "/" + p.ID + ".json"
	if _, err := w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(w.bucket), Key: aws.String(key), Body: bytes.NewReader(body), ContentType: aws.String("application/json")}); err != nil {
		return fmt.Errorf("writing s3://%s/%s: %w", w.bucket, key, err)
	}
	if err := w.trace(ctx, p.ID, rec.MessageId); err != nil {
		return err
	}
	logLine(map[string]string{"event": "processed", "id": p.ID, "messageId": rec.MessageId, "function": w.name, "receiveCount": rec.Attributes["ApproximateReceiveCount"]})
	return nil
}

// trace writes one event to the message's own stream, trace-<id>.
func (w *worker) trace(ctx context.Context, id, messageID string) error {
	stream := "trace-" + id
	_, err := w.logs.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(w.logGroup), LogStreamName: aws.String(stream)})
	if _, exists := errors.AsType[*logstypes.ResourceAlreadyExistsException](err); err != nil && !exists {
		return fmt.Errorf("creating log stream %s: %w", stream, err)
	}
	line, err := json.Marshal(map[string]string{"event": "traced", "id": id, "messageId": messageID, "function": w.name})
	if err != nil {
		return err
	}
	_, err = w.logs.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(w.logGroup),
		LogStreamName: aws.String(stream),
		LogEvents:     []logstypes.InputLogEvent{{Message: aws.String(string(line)), Timestamp: aws.Int64(time.Now().UnixMilli())}},
	})
	if err != nil {
		return fmt.Errorf("writing to log stream %s: %w", stream, err)
	}
	return nil
}

func idOf(rec events.SQSMessage) string {
	var p payload
	if json.Unmarshal([]byte(rec.Body), &p) != nil {
		return ""
	}
	return p.ID
}

func logLine(fields map[string]string) {
	b, err := json.Marshal(fields)
	if err != nil {
		fmt.Printf("goworker: encoding a log line: %v\n", err)
		return
	}
	fmt.Println(string(b))
}
