package s3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// bindNotification binds the event notification operations.
func (s *Service) bindNotification(b s3api.Binder) {
	get := func(in *s3api.GetBucketNotificationConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.GetBucketNotificationConfiguration(reader(s, get, getNotification), registry.Full{}, touches(get))
	put := func(in *s3api.PutBucketNotificationConfigurationInput) access {
		return at(in.Bucket, in.ExpectedBucketOwner)
	}
	b.PutBucketNotificationConfiguration(s.putNotification, registry.Partial{Note: "deliveries to SQS, SNS and Lambda; an EventBridge configuration is stored but sends nothing; destination validation checks existence and refuses FIFO queues and topics, not permissions"}, touches(put))
}

// notificationPut is a configuration that passed admission and its own
// checks, with the home of the bucket it is for.
type notificationPut struct {
	cfg  notify.Config
	home Home
}

func getNotification(_ *s3api.GetBucketNotificationConfigurationInput, b *Bucket) (*s3api.NotificationConfiguration, *awserr.Error) {
	if b.Notification == nil {
		return s3api.NotConfiguredGetBucketNotificationConfiguration(), nil
	}
	return b.Notification, nil
}

// putNotification replaces the bucket's whole configuration (s3.md 13.1).
// The request is admitted (owner, region) and the configuration checked on
// its own before any destination lookup. Unless the request skips
// destination validation, every destination must then exist, and each
// queue and topic is sent s3:TestEvent in the same commit that stores the
// configuration.
func (s *Service) putNotification(ctx context.Context, c *kernel.Call, in *s3api.PutBucketNotificationConfigurationInput) (*s3api.PutBucketNotificationConfigurationOutput, *awserr.Error) {
	a := at(in.Bucket, in.ExpectedBucketOwner)
	put, err := read(ctx, s, c, a, func(b *Bucket) (notificationPut, *awserr.Error) {
		cfg, err := notify.Parse(b.Name, in.NotificationConfiguration)
		if err == nil {
			err = cfg.InRegion(b.Home.Region.String())
		}
		if err != nil {
			return notificationPut{}, configError(err)
		}
		return notificationPut{cfg: cfg, home: b.Home}, nil
	})
	if err != nil {
		return nil, err
	}
	cfg := put.cfg
	validate := in.SkipDestinationValidation == nil || !*in.SkipDestinationValidation
	if validate {
		if err := s.checkDestinations(ctx, c, in.Bucket, &cfg); err != nil {
			return nil, inRegion(err, put.home)
		}
	}
	// writeObjects is the bucket write that hands fn the Tx and the call,
	// which the test events need; the configuration releases no blob.
	_, _, err = writeObjects(ctx, s, c, a, func(b *Bucket, w objectTx) (struct{}, []blob.Ref, *awserr.Error) {
		if cfg.Empty() {
			b.Notification = nil
			return struct{}{}, nil, nil
		}
		wire := cfg.Wire()
		b.Notification = &wire
		if !validate {
			return struct{}{}, nil, nil
		}
		tests, terr := cfg.TestRequests(notifyBucket(b), w.now.Wire(), w.request)
		if terr != nil {
			return struct{}{}, nil, awserr.Internalf(terr)
		}
		for i := range tests {
			w.tx.Deliver(tests[i])
		}
		return struct{}{}, nil, nil
	})
	if err != nil {
		return nil, err
	}
	return &s3api.PutBucketNotificationConfigurationOutput{}, nil
}

// configError answers a configuration notify.Parse or Config.InRegion
// refused.
func configError(err error) *awserr.Error {
	var (
		arn      *notify.ARNError
		event    *notify.EventError
		name     *notify.FilterNameError
		rule     *notify.FilterRuleError
		repeated *notify.RepeatedFilterError
		overlap  *notify.OverlapError
		region   *notify.RegionError
	)
	switch {
	case errors.As(err, &overlap) && overlap.Overlap == notify.SameFilter:
		return s3api.ErrInvalidArgument("Configurations overlap. Configurations on the same bucket cannot share a common event type.")
	case errors.As(err, &overlap):
		return s3api.ErrInvalidArgument("Configuration is ambiguously defined. Cannot have overlapping suffixes in two rules if the prefixes are overlapping for the same event type.")
	case errors.As(err, &region):
		return s3api.ErrInvalidArgument("The notification destination service region is not valid for the bucket location constraint")
	case errors.As(err, &arn):
		return errInvalidArgument("The ARN could not be parsed", arn.Kind.Element(), arn.Value)
	case errors.As(err, &event):
		return errInvalidArgument("The event is not supported for notifications", "Event", event.Event)
	case errors.As(err, &name):
		return errInvalidArgument("filter rule name must be either prefix or suffix", "FilterRule.Name", name.Name)
	case errors.As(err, &rule):
		return errMalformedXML()
	case errors.As(err, &repeated):
		return errInvalidArgument("Cannot specify more than one "+repeated.Part.String()+" rule in a filter.", "FilterRule.Name", repeated.Part.String())
	}
	return awserr.Internalf(err)
}

// unvalidated is a destination that failed validation, as the numbered
// ArgumentName and ArgumentValue pair names it.
type unvalidated struct {
	name, value string
}

// checkDestinations looks up every destination as S3 would before it
// accepts the configuration, and refuses it naming each one that does not
// exist or is a FIFO queue or topic, which S3 does not deliver to (s3.md
// 13.1). Phase 1 enforces no IAM, so the permission half of AWS's check is
// not made.
func (s *Service) checkDestinations(ctx context.Context, c *kernel.Call, bucket string, cfg *notify.Config) *awserr.Error {
	var failed []unvalidated
	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		if fifo, ok := fifoOf(r); ok {
			failed = append(failed, fifo)
			continue
		}
		exists, err := s.destinationExists(ctx, c, bucket, r)
		if err != nil {
			return err
		}
		if !exists {
			failed = append(failed, unvalidatedOf(r))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	e := s3api.ErrInvalidArgument("Unable to validate the following destination configurations")
	for i, f := range failed {
		n := strconv.Itoa(i + 1)
		e = withField(withField(e, "ArgumentName"+n, f.name), "ArgumentValue"+n, f.value)
	}
	return e
}

// unvalidatedOf is the recorded wording of a missing destination; a
// missing function reads as a permission failure, with ", null" after its
// ARN (AWS-recorded lambda-does-not-exist).
func unvalidatedOf(r *notify.Rule) unvalidated {
	arn := r.ARN.String()
	switch r.Kind {
	case notify.Topic:
		return unvalidated{name: arn, value: "The destination topic does not exist"}
	case notify.Function:
		return unvalidated{name: arn + ", null", value: "Not authorized to invoke function [" + arn + "]"}
	case notify.Queue:
	}
	return unvalidated{name: arn, value: "The destination queue does not exist"}
}

// fifoOf is the refusal of a FIFO queue or topic, whose name ends in
// .fifo; the wording is inferred, as no recording shows it.
func fifoOf(r *notify.Rule) (unvalidated, bool) {
	if !strings.HasSuffix(r.ARN.Resource, ".fifo") {
		return unvalidated{}, false
	}
	switch r.Kind {
	case notify.Queue:
		return unvalidated{name: r.ARN.String(), value: "The destination queue is a FIFO queue, which S3 event notifications do not support"}, true
	case notify.Topic:
		return unvalidated{name: r.ARN.String(), value: "The destination topic is a FIFO topic, which S3 event notifications do not support"}, true
	case notify.Function:
	}
	return unvalidated{}, false
}

// destinationExists looks the destination up in its own account and
// region as S3's service principal. Only the destination service's
// not-found answer means "does not exist"; any other failure (an injected
// fault, an emulator bug) is the call's InternalError.
func (s *Service) destinationExists(ctx context.Context, c *kernel.Call, bucket string, r *notify.Rule) (bool, *awserr.Error) {
	scope, serr := aws.ScopeOf(r.ARN)
	if serr != nil {
		return false, nil
	}
	t := kernel.Target{Scope: scope, As: kernel.AsService{Name: "s3.amazonaws.com", Source: bucketARN(bucket)}, Via: ref.S3Notification}
	var (
		err      *awserr.Error
		notFound awserr.Identity
	)
	switch r.Kind {
	case notify.Queue:
		_, err = kernel.Invoke(ctx, s.k, c, sqsapi.OpGetQueueUrl, t, &sqsapi.GetQueueUrlInput{QueueName: r.ARN.Resource, QueueOwnerAWSAccountId: new(r.ARN.Account)})
		notFound = sqsapi.IdentityQueueDoesNotExist()
	case notify.Topic:
		_, err = kernel.Invoke(ctx, s.k, c, snsapi.OpGetTopicAttributes, t, &snsapi.GetTopicAttributesInput{TopicArn: r.ARN.String()})
		notFound = snsapi.IdentityNotFoundException()
	case notify.Function:
		_, err = kernel.Invoke(ctx, s.k, c, lambdaapi.OpGetFunction, t, &lambdaapi.GetFunctionInput{FunctionName: r.ARN.String()})
		notFound = lambdaapi.IdentityResourceNotFoundException()
	}
	switch {
	case err == nil:
		return true, nil
	case err.ID == notFound:
		return false, nil
	}
	if _, internal := err.Origin.(awserr.Internal); internal || err.ID == awserr.RequestAbortedException() {
		return false, err
	}
	return false, awserr.Internalf(&DestinationCheckError{Destination: r.ARN.String(), Code: string(err.ID.Shape), Message: err.Message})
}

// DestinationCheckError is a destination lookup that failed other than
// with not-found, so S3 cannot tell whether the destination exists.
type DestinationCheckError struct {
	Destination string
	Code        string
	Message     string
}

func (e *DestinationCheckError) Error() string {
	return fmt.Sprintf("s3: looking up notification destination %s failed with %s (%s); the configuration was not stored, retry the request", e.Destination, e.Code, e.Message)
}

// requestOf is the call as a notification echoes it: x-amz-request-id and
// x-amz-id-2 as the response carried them, the caller's principal id, and
// the loopback address understudy listens on, as the call carries no peer
// address.
func requestOf(c *kernel.Call) notify.Request {
	id := string(c.ID())
	return notify.Request{ID: id, HostID: restxml.HostID(id), Principal: principalID(c.Principal()), SourceIP: "127.0.0.1"}
}

// principalID is userIdentity.principalId: "AWS:" and the caller's unique
// id (the account id for root, AROA id and session for a role session),
// a service principal's name, or ANONYMOUS_PRINCIPAL.
func principalID(p identity.Principal) string {
	switch p := p.(type) {
	case identity.Root:
		return "AWS:" + p.Account.String()
	case identity.User:
		return "AWS:" + p.ID
	case identity.AssumedRole:
		return "AWS:" + p.Role.ID + ":" + p.Session
	case identity.ServicePrincipal:
		return p.Name
	case identity.Anonymous:
		return "ANONYMOUS_PRINCIPAL"
	}
	return ""
}

// notifyBucket is b as its notifications name it.
func notifyBucket(b *Bucket) notify.Bucket {
	return notify.Bucket{Name: b.Name, ARN: bucketARN(b.Name), Owner: canonicalID(b.Home.OwnerAccount)}
}

// event is an object change that event notifications report: the object
// it changed and what happened to it. The sequencer of a Created or
// Removed change is left zero; notify stamps it.
type event struct {
	key       string
	versionID string
	change    notify.Change
}

// notify raises ev to the notification configuration of b that selects
// it, if any (Parse refuses two that select one change), as a Deliver
// effect of the write w that commits the change. The record's time is the
// commit instant, and its sequencer is notify.Next of that instant after
// the bucket's last one: bucket-wide, so it strictly increases per key
// though the clock stands still or is restored to an earlier time, and
// kept in the snapshotted Bucket.
func (s *Service) notify(w objectTx, b *Bucket, ev event) {
	if b.Notification == nil {
		return
	}
	at := w.now.Wire()
	b.Sequencer = notify.Next(b.Sequencer, at)
	reqs, err := notifications(b, ev, at, w.request)
	if err != nil {
		// The change is valid and stays; only its notifications are lost,
		// to a defect in this package. The seam has no context to log with.
		s.log.Error("s3 raised no event notification for a committed object change",
			slog.String("bucket", b.Name), slog.String("key", ev.key), slog.String("error", err.Error()))
		return
	}
	for i := range reqs {
		w.tx.Deliver(reqs[i])
	}
}

// notifications are the deliveries ev raises in b at the given time, for
// the call req, with b's last sequencer.
func notifications(b *Bucket, ev event, at time.Time, req notify.Request) ([]delivery.Request, error) {
	cfg, err := notify.ParseStored(b.Name, *b.Notification)
	if err != nil {
		return nil, fmt.Errorf("the stored notification configuration no longer parses: %w", err)
	}
	version := ev.versionID
	if version == nullVersion {
		version = ""
	}
	return cfg.Requests(notify.Event{
		Time: at, Region: b.Home.Region.String(), Bucket: notifyBucket(b),
		Key: ev.key, VersionID: version, Change: sequenced(ev.change, b.Sequencer), Request: req,
	})
}

// sequenced is c with seq as its sequencer; a Tagged change carries none.
func sequenced(c notify.Change, seq notify.Sequencer) notify.Change {
	switch c := c.(type) {
	case notify.Created:
		c.Sequencer = seq
		return c
	case notify.Removed:
		c.Sequencer = seq
		return c
	case notify.Tagged:
	}
	return c
}
