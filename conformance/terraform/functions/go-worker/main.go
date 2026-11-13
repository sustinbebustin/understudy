// Command bootstrap is the Go worker the Terraform conformance suite deploys
// on the provided.al2023 runtime. It consumes SQS batches and reports the
// messages whose body is not a JSON object as batch item failures, the
// partial-batch contract an event source mapping with ReportBatchItemFailures
// expects.
//
// It speaks the Lambda Runtime API directly with net/http, so the function
// needs nothing beyond the standard library.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

// sqsEvent is the part of the SQS event this worker reads.
type sqsEvent struct {
	Records []struct {
		MessageID string `json:"messageId"`
		Body      string `json:"body"`
	} `json:"Records"`
}

// batchResponse is the partial batch response
// (https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-errorhandling.html).
type batchResponse struct {
	BatchItemFailures []batchItemFailure `json:"batchItemFailures"`
}

type batchItemFailure struct {
	ItemIdentifier string `json:"itemIdentifier"`
}

func main() {
	api := os.Getenv("AWS_LAMBDA_RUNTIME_API")
	if api == "" {
		log.Fatal("AWS_LAMBDA_RUNTIME_API is not set; this binary runs only inside a Lambda execution environment")
	}
	base := "http://" + api + "/2018-06-01/runtime/invocation/"
	ctx := context.Background()
	for {
		if err := serveOne(ctx, base); err != nil {
			log.Fatal(err)
		}
	}
}

// serveOne fetches the next invocation and posts its response or error.
func serveOne(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"next", http.NoBody)
	if err != nil {
		return fmt.Errorf("fetch next invocation: %w", err)
	}
	next, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch next invocation: %w", err)
	}
	payload, err := io.ReadAll(next.Body)
	_ = next.Body.Close()
	if err != nil {
		return fmt.Errorf("read next invocation: %w", err)
	}
	requestID := next.Header.Get("Lambda-Runtime-Aws-Request-Id")

	var event sqsEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return post(ctx, base+requestID+"/error", map[string]string{
			"errorType":    "InvalidEvent",
			"errorMessage": "event is not an SQS batch: " + err.Error(),
		})
	}
	return post(ctx, base+requestID+"/response", handle(event))
}

// handle reports every record whose body is not a JSON object as failed.
func handle(event sqsEvent) batchResponse {
	resp := batchResponse{BatchItemFailures: []batchItemFailure{}}
	for _, r := range event.Records {
		var body map[string]any
		if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
			log.Printf("message %s: body is not a JSON object: %v", r.MessageID, err)
			resp.BatchItemFailures = append(resp.BatchItemFailures, batchItemFailure{ItemIdentifier: r.MessageID})
			continue
		}
		log.Printf("message %s: processed %d fields", r.MessageID, len(body))
	}
	return resp
}

func post(ctx context.Context, url string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode %s: %w", url, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("post %s: %w", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", url, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("post %s: status %d, want 202", url, resp.StatusCode)
	}
	return nil
}
