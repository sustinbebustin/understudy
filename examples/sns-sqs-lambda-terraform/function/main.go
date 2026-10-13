// Command bootstrap is the order worker: a Go Lambda function on the
// provided.al2023 runtime, fed by an SQS event source mapping.
//
// Each SQS record carries one order, published to SNS and delivered raw (the
// body is the order itself, not an SNS envelope). The worker writes every
// valid order to S3 as orders/<orderId>.json. A record it cannot handle is
// reported in batchItemFailures, so only that message returns to the queue;
// the rest of the batch is deleted. That is the ReportBatchItemFailures
// contract the event source mapping enables.
//
// Nothing here knows about understudy: inside a function, understudy sets
// AWS_ENDPOINT_URL, so the SDK's calls come back to the emulator.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// order is the message the webhook publishes.
type order struct {
	OrderID string  `json:"orderId"`
	Amount  float64 `json:"amount"`
}

// parseOrder reads an SQS body as an order, or says why it is not one.
func parseOrder(body string) (order, error) {
	var o order
	if err := json.Unmarshal([]byte(body), &o); err != nil {
		return order{}, fmt.Errorf("the body is not a JSON order: %w", err)
	}
	if o.OrderID == "" {
		return order{}, errors.New(`the order has no "orderId"`)
	}
	return o, nil
}

type worker struct {
	bucket string
	s3     *s3.Client
	log    *slog.Logger
}

func main() {
	// One JSON object per line: CloudWatch Logs filter patterns can then
	// match on fields, such as { $.msg = "order failed" }.
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()
	bucket := os.Getenv("ORDERS_BUCKET")
	if bucket == "" {
		log.ErrorContext(ctx, "ORDERS_BUCKET is not set; set it in the function's environment")
		os.Exit(1)
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.ErrorContext(ctx, "loading the AWS SDK configuration", slog.Any("error", err))
		os.Exit(1)
	}
	w := worker{bucket: bucket, s3: s3.NewFromConfig(cfg), log: log}
	lambda.Start(w.handle)
}

// handle processes one batch and names the records that failed.
func (w worker) handle(ctx context.Context, event events.SQSEvent) (events.SQSEventResponse, error) {
	var resp events.SQSEventResponse
	for i := range event.Records {
		rec := &event.Records[i]
		key, err := w.process(ctx, rec)
		if err != nil {
			w.log.WarnContext(ctx, "order failed",
				slog.String("message_id", rec.MessageId),
				slog.String("receive_count", rec.Attributes["ApproximateReceiveCount"]),
				slog.Any("error", err))
			resp.BatchItemFailures = append(resp.BatchItemFailures, events.SQSBatchItemFailure{ItemIdentifier: rec.MessageId})
			continue
		}
		w.log.InfoContext(ctx, "order stored", slog.String("message_id", rec.MessageId), slog.String("key", key))
	}
	return resp, nil
}

// process stores one order and returns its object key.
func (w worker) process(ctx context.Context, rec *events.SQSMessage) (string, error) {
	o, err := parseOrder(rec.Body)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(o)
	if err != nil {
		return "", fmt.Errorf("encoding order %s: %w", o.OrderID, err)
	}
	key := "orders/" + o.OrderID + ".json"
	_, err = w.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(w.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return "", fmt.Errorf("writing s3://%s/%s: %w", w.bucket, key, err)
	}
	return key, nil
}
