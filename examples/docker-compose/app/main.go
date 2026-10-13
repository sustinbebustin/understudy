// Command app is the application container of the docker compose example.
// It stands in for a real service: it stores a note in S3, queues a job
// naming it on SQS, then takes the job off the queue, reads the note back and
// deletes the job. Last it counts the notes in the bucket, which grows by one
// per run while understudy keeps its state in its volume.
//
// The SDK finds understudy through AWS_ENDPOINT_URL, which compose.yaml sets
// to the service name, http://understudy:4566. Nothing else in the code is
// specific to understudy except path-style S3 addressing (see newS3).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const (
	bucket    = "compose-demo"
	queueName = "compose-demo-jobs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "app: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading the AWS SDK configuration: %w", err)
	}
	s3c := newS3(cfg)
	sqsc := sqs.NewFromConfig(cfg)

	// Both creates are safe to repeat: S3 answers BucketAlreadyOwnedByYou for
	// a bucket this account already has, and CreateQueue returns the
	// existing queue when the attributes match.
	_, err = s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	var owned *s3types.BucketAlreadyOwnedByYou
	if err != nil && !errors.As(err, &owned) {
		return fmt.Errorf("creating bucket %s: %w", bucket, err)
	}
	queue, err := sqsc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	if err != nil {
		return fmt.Errorf("creating queue %s: %w", queueName, err)
	}

	key := "notes/" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".txt"
	_, err = s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader("hello from the app container"),
	})
	if err != nil {
		return fmt.Errorf("writing s3://%s/%s: %w", bucket, key, err)
	}
	if _, err := sqsc.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: queue.QueueUrl, MessageBody: aws.String(key)}); err != nil {
		return fmt.Errorf("sending the job for %s: %w", key, err)
	}
	fmt.Printf("stored s3://%s/%s and queued a job for it on %s\n", bucket, key, aws.ToString(queue.QueueUrl))

	// Long polling waits up to WaitTimeSeconds for the job instead of
	// returning empty at once.
	got, err := sqsc.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: queue.QueueUrl, WaitTimeSeconds: 5})
	if err != nil {
		return fmt.Errorf("receiving the job: %w", err)
	}
	if len(got.Messages) == 0 {
		return fmt.Errorf("no job arrived on %s within 5 seconds", aws.ToString(queue.QueueUrl))
	}
	job := got.Messages[0]
	note, err := readObject(ctx, s3c, aws.ToString(job.Body))
	if err != nil {
		return err
	}
	if _, err := sqsc.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: queue.QueueUrl, ReceiptHandle: job.ReceiptHandle}); err != nil {
		return fmt.Errorf("deleting the job: %w", err)
	}
	fmt.Printf("took the job for %s off the queue; the note says %q\n", aws.ToString(job.Body), note)

	listed, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("notes/")})
	if err != nil {
		return fmt.Errorf("listing s3://%s/notes/: %w", bucket, err)
	}
	fmt.Printf("the bucket holds %d note(s)\n", aws.ToInt32(listed.KeyCount))
	return nil
}

// newS3 makes an S3 client that puts the bucket in the path
// (http://understudy:4566/compose-demo/key) rather than in the hostname
// (http://compose-demo.understudy:4566/key), which no DNS server in the
// compose network resolves. boto3 and the AWS CLI do this by themselves for
// a custom endpoint; the Go and JavaScript SDKs need to be told.
func newS3(cfg aws.Config) *s3.Client {
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
}

func readObject(ctx context.Context, c *s3.Client, key string) (string, error) {
	obj, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", fmt.Errorf("reading s3://%s/%s: %w", bucket, key, err)
	}
	defer func() { _ = obj.Body.Close() }()
	body, err := io.ReadAll(obj.Body)
	if err != nil {
		return "", fmt.Errorf("reading s3://%s/%s: %w", bucket, key, err)
	}
	return string(body), nil
}
