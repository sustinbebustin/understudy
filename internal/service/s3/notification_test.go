package s3_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	idsource "github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

const (
	// notifyBucket is the bucket runNotifier creates.
	notifyBucket   = "uploads"
	notifyQueue    = "arn:aws:sqs:us-east-1:000000000000:uploads"
	notifyTopic    = "arn:aws:sns:us-east-1:000000000000:uploads"
	notifyFunction = "arn:aws:lambda:us-east-1:000000000000:function:thumbnail"
)

// notifier is a kernel running S3 with a DeliverSink that records every
// accepted delivery, and stub SQS, SNS and Lambda lookups that know the
// destinations above. kerneltest builds its kernel without a DeliverSink,
// so this harness builds its own.
type notifier struct {
	*testing.T

	k     *kernel.Kernel
	clock *clock.Clock
	sink  *recordingSink
}

// recordingSink is the outbox as the kernel sees it: it accepts and records.
type recordingSink struct {
	mu  sync.Mutex
	got []kernel.Delivery
}

func (s *recordingSink) Deliver(d kernel.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.got = append(s.got, d)
	return nil
}

// take returns the deliveries accepted since the last take.
func (s *recordingSink) take() []kernel.Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.got
	s.got = nil
	return out
}

func runNotifier(t *testing.T, fn func(n *notifier)) {
	t.Helper()
	synctest.Test(t, func(t *testing.T) {
		n := buildNotifier(t)
		ctx, cancel := context.WithCancel(context.Background())
		stopped := make(chan error, 1)
		go func() { stopped <- n.k.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-stopped; err != nil {
				t.Errorf("kernel.Run = %v", err)
			}
			if err := n.k.BindErr(); err != nil {
				t.Errorf("bind mistakes: %v", err)
			}
		}()
		notifyMust(n, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: notifyBucket})
		fn(n)
	})
}

func buildNotifier(t *testing.T) *notifier {
	t.Helper()
	timers := kernel.NewTimers()
	clk, err := clock.New(clock.Options{Mode: clock.Frozen{}, Timers: timers.Scheduler()})
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.New(journal.Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := registry.New(s3api.Service(), sqsapi.Service(), snsapi.Service(), lambdaapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	src := idsource.NewSeeded(1)
	idStore := identity.New(src, aws.DefaultAccount())
	sink := &recordingSink{}
	k, err := kernel.New(kernel.Options{
		Registry: reg, Clock: clk, Timers: timers, IDs: src, Journal: j,
		Identity: idStore, Deliver: sink, Strict: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := blob.NewMemory(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := blobs.Close(); err != nil {
			t.Error(err)
		}
	})
	s3.New(kernel.ServiceDeps{Kernel: k, Binder: k.Binder(s3api.Service()), Clock: clk, IDs: src, Identity: idStore, Log: k.Log(), Blob: blobs}, s3.Options{})
	kernel.Bind(k.Binder(sqsapi.Service()), sqsapi.OpGetQueueUrl, func(_ context.Context, _ *kernel.Call, in *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
		if "arn:aws:sqs:us-east-1:"+*in.QueueOwnerAWSAccountId+":"+in.QueueName != notifyQueue {
			return nil, sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
		}
		return &sqsapi.GetQueueUrlOutput{QueueUrl: new("http://localhost:4566/000000000000/uploads")}, nil
	}, registry.Full{})
	kernel.Bind(k.Binder(snsapi.Service()), snsapi.OpGetTopicAttributes, func(_ context.Context, _ *kernel.Call, in *snsapi.GetTopicAttributesInput) (*snsapi.GetTopicAttributesOutput, *awserr.Error) {
		if in.TopicArn != notifyTopic {
			return nil, snsapi.ErrNotFoundException("Topic does not exist")
		}
		return &snsapi.GetTopicAttributesOutput{}, nil
	}, registry.Full{})
	kernel.Bind(k.Binder(lambdaapi.Service()), lambdaapi.OpGetFunction, func(_ context.Context, _ *kernel.Call, in *lambdaapi.GetFunctionInput) (*lambdaapi.GetFunctionOutput, *awserr.Error) {
		if in.FunctionName != notifyFunction {
			return nil, lambdaapi.ErrResourceNotFoundException("Function not found: " + in.FunctionName)
		}
		return &lambdaapi.GetFunctionOutput{}, nil
	}, registry.Full{})
	return &notifier{T: t, k: k, clock: clk, sink: sink}
}

// notifyCall runs op with in as the default account's root in us-east-1 and
// returns the output, the error and the request id the call answered with.
func notifyCall[In, Out any](n *notifier, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error, idsource.RequestID) {
	id := n.k.IDs().RequestID()
	scope := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	c := n.k.NewCall(kernel.ExternalCall{
		RequestID:    id,
		Operation:    op.Descriptor().ID,
		Scope:        scope,
		RegionSource: aws.CredentialScope{Region: scope.Region},
		Principal:    identity.Root{Account: scope.Account},
		Origin:       ref.External{Protocol: ref.RESTXML, Listener: ref.ListenerMain},
		Admitted:     n.clock.Now(),
		Endpoint:     kernel.Endpoint{Scheme: "http", Host: "localhost:4566"},
	})
	switch r := n.k.Dispatch(n.Context(), c, kernel.Ok{In: in}).(type) {
	case kernel.Output:
		out, ok := r.Out.(*Out)
		if !ok {
			n.Fatalf("%s returned %T", op.Descriptor().ID, r.Out)
		}
		return out, nil, id
	case kernel.Failure:
		return nil, r.Err, id
	case kernel.StatusOKFailure:
		return nil, r.Err, id
	}
	n.Fatalf("%s returned no result", op.Descriptor().ID)
	return nil, nil, id
}

func notifyMust[In, Out any](n *notifier, op kernel.Op[In, Out], in *In) *Out {
	n.Helper()
	out, err, _ := notifyCall(n, op, in)
	if err != nil {
		n.Fatalf("%s: %s: %s", op.Descriptor().ID, err.ID.Shape, err.Message)
	}
	return out
}

// configure puts cfg on the bucket, skipping destination validation when
// skip is set.
func (n *notifier) configure(cfg s3api.NotificationConfiguration, skip bool) *awserr.Error {
	n.Helper()
	in := &s3api.PutBucketNotificationConfigurationInput{Bucket: notifyBucket, NotificationConfiguration: cfg}
	if skip {
		in.SkipDestinationValidation = new(true)
	}
	_, err, _ := notifyCall(n, s3api.OpPutBucketNotificationConfiguration, in)
	return err
}

func (n *notifier) stored() *s3api.NotificationConfiguration {
	n.Helper()
	return notifyMust(n, s3api.OpGetBucketNotificationConfiguration, &s3api.GetBucketNotificationConfigurationInput{Bucket: notifyBucket})
}

func (n *notifier) put(key string) {
	n.Helper()
	notifyMust(n, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: notifyBucket, Key: key, Body: bytesBody("abc"), ContentLength: new(int64(3))})
}

func created() []s3api.Event { return []s3api.Event{s3api.EventS3Objectcreated} }

func keyFilter(rules ...s3api.FilterRule) *s3api.NotificationConfigurationFilter {
	return &s3api.NotificationConfigurationFilter{Key: &s3api.S3KeyFilter{FilterRules: rules}}
}

func filterRule(name, value string) s3api.FilterRule {
	return s3api.FilterRule{Name: new(s3api.FilterRuleName(name)), Value: new(value)}
}

// GetBucketNotificationConfiguration returns what Put stored, rule names
// capitalized and a generated Id where none was sent; an empty
// configuration turns notifications off.
func TestNotificationConfigurationRoundTrips(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		if err := n.configure(s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{
			QueueArn: notifyQueue, Events: created(), Filter: keyFilter(filterRule("prefix", "in/"), filterRule("suffix", "")),
		}}}, true); err != nil {
			n.Fatalf("Put: %s", err.Message)
		}
		got := n.stored()
		if len(got.QueueConfigurations) != 1 {
			n.Fatalf("queue configurations = %d, want 1", len(got.QueueConfigurations))
		}
		q := got.QueueConfigurations[0]
		if q.Id == nil || *q.Id == "" || q.QueueArn != notifyQueue {
			n.Fatalf("stored = %+v, want the queue with a generated Id", q)
		}
		rules := q.Filter.Key.FilterRules
		if len(rules) != 2 || *rules[0].Name != "Prefix" || *rules[0].Value != "in/" || *rules[1].Name != "Suffix" || *rules[1].Value != "" {
			n.Fatalf("filter rules = %+v, want Prefix=in/ and Suffix=", rules)
		}
		if err := n.configure(s3api.NotificationConfiguration{}, false); err != nil {
			n.Fatalf("clearing Put: %s", err.Message)
		}
		if got = n.stored(); len(got.QueueConfigurations) != 0 {
			n.Fatalf("after clearing: %+v, want the empty configuration", got)
		}
		n.put("in/a")
		if d := n.sink.take(); len(d) != 0 {
			n.Fatalf("a cleared configuration delivered %d notifications", len(d))
		}
	})
}

// The refusals S3 answers before it stores anything (s3.md 13.1), each
// naming the bucket's region.
func TestNotificationConfigurationRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg     s3api.NotificationConfiguration
		code    string
		fields  map[string]string
		message string // checked when set
	}{
		"two unfiltered configurations sharing an event": {
			cfg: s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{
				{QueueArn: notifyQueue, Events: created()},
				{QueueArn: notifyQueue, Events: []s3api.Event{s3api.EventS3ObjectcreatedPut}},
			}},
			code:    "InvalidArgument",
			message: "Configurations overlap. Configurations on the same bucket cannot share a common event type.",
		},
		"overlapping suffixes for one event": {
			cfg: s3api.NotificationConfiguration{
				TopicConfigurations: []s3api.TopicConfiguration{{TopicArn: notifyTopic, Events: created(), Filter: keyFilter(filterRule("suffix", "jpg"))}},
				QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created(), Filter: keyFilter(filterRule("suffix", "pg"))}},
			},
			code:    "InvalidArgument",
			message: "Configuration is ambiguously defined. Cannot have overlapping suffixes in two rules if the prefixes are overlapping for the same event type.",
		},
		"destination in another region": {
			cfg:     s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: "arn:aws:sqs:us-west-2:000000000000:uploads", Events: created()}}},
			code:    "InvalidArgument",
			message: "The notification destination service region is not valid for the bucket location constraint",
		},
		"malformed queue ARN": {
			cfg:    s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: "bad", Events: created()}}},
			code:   "InvalidArgument",
			fields: map[string]string{"ArgumentName": "Queue", "ArgumentValue": "bad"},
		},
		"malformed function ARN": {
			cfg:    s3api.NotificationConfiguration{LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: "arn:aws:sqs:us-east-1:000000000000:q", Events: created()}}},
			code:   "InvalidArgument",
			fields: map[string]string{"ArgumentName": "CloudFunction", "ArgumentValue": "arn:aws:sqs:us-east-1:000000000000:q"},
		},
		"two prefix rules": {
			cfg:    s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created(), Filter: keyFilter(filterRule("prefix", "a"), filterRule("prefix", "b"))}}},
			code:   "InvalidArgument",
			fields: map[string]string{"ArgumentName": "FilterRule.Name", "ArgumentValue": "prefix"},
		},
		"filter rule without a value": {
			cfg:  s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created(), Filter: keyFilter(s3api.FilterRule{Name: new(s3api.FilterRuleNamePrefix)})}}},
			code: "MalformedXML",
		},
	} {
		t.Run(name, func(t *testing.T) {
			runNotifier(t, func(n *notifier) {
				err := n.configure(tc.cfg, true)
				if err == nil || err.ID.QueryCode != tc.code {
					n.Fatalf("Put = %v, want %s", err, tc.code)
				}
				if tc.message != "" && err.Message != tc.message {
					n.Fatalf("message = %q, want %q", err.Message, tc.message)
				}
				wantFields(n.T, err, tc.fields)
				if r := header(err, "x-amz-bucket-region"); r != "us-east-1" {
					n.Fatalf("x-amz-bucket-region = %q, want us-east-1", r)
				}
				if len(n.stored().QueueConfigurations) != 0 {
					n.Fatal("a refused configuration was stored")
				}
			})
		})
	}
}

// A Put naming another account as the bucket's owner is refused as access
// denied before its configuration is read or a destination looked up.
func TestNotificationPutIsAdmittedFirst(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		_, err, _ := notifyCall(n, s3api.OpPutBucketNotificationConfiguration, &s3api.PutBucketNotificationConfigurationInput{
			Bucket: notifyBucket, ExpectedBucketOwner: new("111111111111"),
			NotificationConfiguration: s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: "bad", Events: created()}}},
		})
		if err == nil || err.ID.QueryCode != "AccessDenied" {
			n.Fatalf("Put = %v, want AccessDenied", err)
		}
	})
}

// FIFO queues and topics fail destination validation, named in numbered
// pairs like a missing destination, without a lookup.
func TestDestinationValidationRefusesFIFODestinations(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		fifoQueue := "arn:aws:sqs:us-east-1:000000000000:uploads.fifo"
		fifoTopic := "arn:aws:sns:us-east-1:000000000000:uploads.fifo"
		err := n.configure(s3api.NotificationConfiguration{
			TopicConfigurations: []s3api.TopicConfiguration{{TopicArn: fifoTopic, Events: created()}},
			QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: fifoQueue, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
		}, false)
		if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Message != "Unable to validate the following destination configurations" {
			n.Fatalf("Put = %v, want the destination validation failure", err)
		}
		wantFields(n.T, err, map[string]string{
			"ArgumentName1": fifoTopic, "ArgumentValue1": "The destination topic is a FIFO topic, which S3 event notifications do not support",
			"ArgumentName2": fifoQueue, "ArgumentValue2": "The destination queue is a FIFO queue, which S3 event notifications do not support",
		})
	})
}

// Destination validation names every destination that does not exist, in
// numbered pairs (AWS-recorded test_multiple_invalid_sqs_arns,
// lambda-does-not-exist), and stores nothing.
func TestDestinationValidationNamesEveryMissingDestination(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		missingQueue := "arn:aws:sqs:us-east-1:000000000000:gone"
		missingFunction := "arn:aws:lambda:us-east-1:000000000000:function:gone"
		err := n.configure(s3api.NotificationConfiguration{
			QueueConfigurations:          []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created()}, {QueueArn: missingQueue, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
			LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: missingFunction, Events: []s3api.Event{s3api.EventS3Objecttagging}}},
		}, false)
		if err == nil || err.ID.QueryCode != "InvalidArgument" || err.Message != "Unable to validate the following destination configurations" {
			n.Fatalf("Put = %v, want the destination validation failure", err)
		}
		wantFields(n.T, err, map[string]string{
			"ArgumentName1": missingQueue, "ArgumentValue1": "The destination queue does not exist",
			"ArgumentName2": missingFunction + ", null", "ArgumentValue2": "Not authorized to invoke function [" + missingFunction + "]",
		})
		if len(n.stored().QueueConfigurations) != 0 || len(n.sink.take()) != 0 {
			n.Fatal("a refused configuration was stored or sent a test event")
		}
	})
}

// A validated configuration sends s3:TestEvent to its topic and queue,
// echoing the Put's request id; the function gets none, and a Put that
// skips validation sends none.
func TestValidatedConfigurationSendsTestEvents(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		cfg := s3api.NotificationConfiguration{
			TopicConfigurations:          []s3api.TopicConfiguration{{TopicArn: notifyTopic, Events: created()}},
			QueueConfigurations:          []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
			LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: notifyFunction, Events: []s3api.Event{s3api.EventS3Objecttagging}}},
		}
		_, err, id := notifyCall(n, s3api.OpPutBucketNotificationConfiguration, &s3api.PutBucketNotificationConfigurationInput{Bucket: notifyBucket, NotificationConfiguration: cfg})
		if err != nil {
			n.Fatalf("Put: %s", err.Message)
		}
		got := n.sink.take()
		if len(got) != 2 || got[0].Request.Mechanism != delivery.S3ToSNS || got[1].Request.Mechanism != delivery.S3ToSQS {
			n.Fatalf("test deliveries = %+v, want the topic then the queue", got)
		}
		q, ok := got[1].Request.Target.(delivery.SQSQueue)
		if !ok {
			n.Fatalf("queue target = %T", got[1].Request.Target)
		}
		var ev struct {
			Event     string
			Bucket    string
			RequestID string `json:"RequestId"`
		}
		if err := json.Unmarshal([]byte(q.Body), &ev); err != nil {
			n.Fatal(err)
		}
		if ev.Event != "s3:TestEvent" || ev.Bucket != notifyBucket || ev.RequestID != string(id) {
			n.Fatalf("test event = %+v, want s3:TestEvent for the bucket with request id %s", ev, id)
		}
		if err := n.configure(cfg, true); err != nil {
			n.Fatalf("Put skipping validation: %s", err.Message)
		}
		if d := n.sink.take(); len(d) != 0 {
			n.Fatalf("a Put skipping validation sent %d test events", len(d))
		}
	})
}

// Each object change raises one delivery, to the configuration whose event
// types and filters select it, with the mechanism of its destination; a
// change no configuration selects raises none.
func TestObjectChangesRaiseMatchingDeliveries(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		if err := n.configure(s3api.NotificationConfiguration{
			TopicConfigurations: []s3api.TopicConfiguration{{Id: new("docs"), TopicArn: notifyTopic, Events: created(), Filter: keyFilter(filterRule("prefix", "docs/"), filterRule("suffix", ".txt"))}},
			QueueConfigurations: []s3api.QueueConfiguration{
				{Id: new("in"), QueueArn: notifyQueue, Events: created(), Filter: keyFilter(filterRule("prefix", "in/"))},
				{Id: new("gone"), QueueArn: notifyQueue, Events: []s3api.Event{s3api.EventS3ObjectremovedDelete}, Filter: keyFilter(filterRule("prefix", "in/"))},
			},
			LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{Id: new("out"), LambdaFunctionArn: notifyFunction, Events: []s3api.Event{s3api.EventS3ObjectcreatedPut}, Filter: keyFilter(filterRule("prefix", "out/"))}},
		}, true); err != nil {
			n.Fatalf("Put: %s", err.Message)
		}
		for _, tc := range []struct {
			key, encoded string
			mechanism    delivery.Mechanism
			config       string
		}{
			{"docs/a b.txt", "docs/a+b.txt", delivery.S3ToSNS, "docs"},
			{"in/a b.txt", "in/a+b.txt", delivery.S3ToSQS, "in"},
			{"out/a.bin", "out/a.bin", delivery.S3ToLambda, "out"},
		} {
			n.put(tc.key)
			got := n.sink.take()
			if len(got) != 1 {
				n.Fatalf("a put of %s raised %d deliveries, want 1", tc.key, len(got))
			}
			d := got[0]
			rec := recordOf(n, d.Request.Target)
			if d.Request.Mechanism != tc.mechanism || rec.S3.ConfigurationID != tc.config || d.Request.Policy != (delivery.S3Notification{}) {
				n.Fatalf("delivery of %s = %s for %q, want %s for %q", tc.key, d.Request.Mechanism, rec.S3.ConfigurationID, tc.mechanism, tc.config)
			}
			if rec.EventName != "ObjectCreated:Put" || rec.S3.Object.Key != tc.encoded || rec.S3.Object.ETag != "900150983cd24fb0d6963f7d28e17f72" || rec.AWSRegion != "us-east-1" {
				n.Fatalf("record of %s = %+v", tc.key, rec)
			}
		}

		n.put("docs/a.bin")
		if got := n.sink.take(); len(got) != 0 {
			n.Fatalf("a put no configuration selects raised %d deliveries", len(got))
		}

		notifyMust(n, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: notifyBucket, Key: "in/a b.txt"})
		got := n.sink.take()
		if len(got) != 1 {
			n.Fatalf("delete raised %d deliveries, want the removal", len(got))
		}
		if rec := recordOf(n, got[0].Request.Target); rec.EventName != "ObjectRemoved:Delete" || rec.S3.ConfigurationID != "gone" {
			n.Fatalf("removal = %+v", rec)
		}
	})
}

// An object record echoes the call that made the change: its request id
// and host id, the caller's principal and the source address (s3.md 13.2).
func TestObjectRecordEchoesTheCall(t *testing.T) {
	runNotifier(t, func(n *notifier) {
		if err := n.configure(s3api.NotificationConfiguration{QueueConfigurations: []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created()}}}, true); err != nil {
			n.Fatalf("Put: %s", err.Message)
		}
		_, err, id := notifyCall(n, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: notifyBucket, Key: "a", Body: bytesBody("abc"), ContentLength: new(int64(3))})
		if err != nil {
			n.Fatalf("PutObject: %s", err.Message)
		}
		got := n.sink.take()
		if len(got) != 1 {
			n.Fatalf("deliveries = %d, want 1", len(got))
		}
		rec := recordOf(n, got[0].Request.Target)
		want := s3Call{RequestID: string(id), HostID: restxml.HostID(string(id)), Principal: "AWS:000000000000", SourceIP: "127.0.0.1"}
		if have := (s3Call{rec.ResponseElements.RequestID, rec.ResponseElements.HostID, rec.UserIdentity.PrincipalID, rec.RequestParameters.SourceIP}); have != want {
			n.Fatalf("record echoes %+v, want %+v", have, want)
		}
	})
}

type s3Call struct{ RequestID, HostID, Principal, SourceIP string }

// The sequencers of one key's events strictly increase, compared as hex
// left-padded to one length, though the clock stands still (frozen) or is
// restored to an earlier time (s3.md 13.2, ARCHITECTURE.md 16).
func TestSequencersIncreasePerKey(t *testing.T) {
	type op struct {
		kind string // put, delete or rewind
		key  string
	}
	rapid.Check(t, func(rt *rapid.T) {
		ops := rapid.SliceOfN(rapid.Custom(func(rt *rapid.T) op {
			return op{kind: rapid.SampledFrom([]string{"put", "put", "delete", "rewind"}).Draw(rt, "kind"), key: rapid.SampledFrom([]string{"a", "b"}).Draw(rt, "key")}
		}), 2, 12).Draw(rt, "ops")
		seqs := map[string][]string{}
		runNotifier(t, func(n *notifier) {
			if err := n.configure(s3api.NotificationConfiguration{
				QueueConfigurations:          []s3api.QueueConfiguration{{QueueArn: notifyQueue, Events: created()}},
				LambdaFunctionConfigurations: []s3api.LambdaFunctionConfiguration{{LambdaFunctionArn: notifyFunction, Events: []s3api.Event{s3api.EventS3Objectremoved}}},
			}, true); err != nil {
				n.Fatalf("Put: %s", err.Message)
			}
			for _, o := range ops {
				switch o.kind {
				case "put":
					n.put(o.key)
				case "delete":
					notifyMust(n, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: notifyBucket, Key: o.key})
				case "rewind":
					if _, err := n.clock.Restore(n.clock.Now().Add(-time.Second), clock.Frozen{}); err != nil {
						n.Fatal(err)
					}
				}
				for _, d := range n.sink.take() {
					rec := recordOf(n, d.Request.Target)
					seqs[rec.S3.Object.Key] = append(seqs[rec.S3.Object.Key], rec.S3.Object.Sequencer)
				}
			}
		})
		for key, s := range seqs {
			for i := 1; i < len(s); i++ {
				width := max(len(s[i-1]), len(s[i]))
				if padHex(s[i], width) <= padHex(s[i-1], width) {
					rt.Fatalf("key %s: sequencer %s after %s does not increase (all: %v)", key, s[i], s[i-1], s)
				}
			}
		}
	})
}

// padHex left-pads a sequencer with zeros to width, as S3 tells consumers
// to before comparing two.
func padHex(s string, width int) string {
	return strings.Repeat("0", width-len(s)) + s
}

type s3Record struct {
	EventName    string `json:"eventName"`
	AWSRegion    string `json:"awsRegion"`
	UserIdentity struct {
		PrincipalID string `json:"principalId"`
	} `json:"userIdentity"`
	RequestParameters struct {
		SourceIP string `json:"sourceIPAddress"`
	} `json:"requestParameters"`
	ResponseElements struct {
		RequestID string `json:"x-amz-request-id"`
		HostID    string `json:"x-amz-id-2"`
	} `json:"responseElements"`
	S3 struct {
		ConfigurationID string `json:"configurationId"`
		Object          struct {
			Key       string `json:"key"`
			ETag      string `json:"eTag"`
			Sequencer string `json:"sequencer"`
		} `json:"object"`
	} `json:"s3"`
}

// recordOf is the one record a notification to t carries.
func recordOf(n *notifier, t delivery.Target) s3Record {
	n.Helper()
	var body string
	switch t := t.(type) {
	case delivery.SQSQueue:
		body = t.Body
	case delivery.SNSTopic:
		if t.Subject != "Amazon S3 Notification" {
			n.Fatalf("topic subject = %q", t.Subject)
		}
		body = t.Message
	case delivery.LambdaFunction:
		if t.Invocation != delivery.InvocationEvent {
			n.Fatalf("function invocation = %s, want Event", t.Invocation)
		}
		body = string(t.Event)
	}
	var doc struct{ Records []s3Record }
	if err := json.Unmarshal([]byte(body), &doc); err != nil || len(doc.Records) != 1 {
		n.Fatalf("notification %q: %v", body, err)
	}
	if !strings.HasPrefix(body, `{"Records":[{"eventVersion":"2.1"`) {
		n.Fatalf("notification %s does not lead with eventVersion 2.1", body)
	}
	return doc.Records[0]
}
