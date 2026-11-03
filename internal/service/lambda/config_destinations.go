package lambda

import (
	"context"
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
)

// configuredDestination is one side of a DestinationConfig: the ARN as
// sent, which the refusals quote, and the destination it names.
type configuredDestination struct {
	arn  string
	dest destination.Destination
}

// checkDestinations checks a DestinationConfig, OnSuccess before OnFailure.
// First, before any lookup, an ARN that names no destination Lambda
// delivers to, or an S3 bucket on success, which is an on-failure
// destination only, is refused in the recorded words for another
// service's ARN (lambda-destinations-layers.md 2.2); that AWS answers a
// malformed ARN of a service it does deliver to the same way is inferred.
// A FIFO queue is refused the same way (aws-recorded, 2.2: LDL-08); a
// FIFO topic is accepted, and Lambda fails it at delivery (inferred).
// Then each destination must exist and not be the function itself
// (checkDestination). It returns the destinations, which the write checks
// again (recheckDestination).
func (s *Service) checkDestinations(ctx context.Context, c *kernel.Call, fn functionRef, d *lambdaapi.DestinationConfig) ([]configuredDestination, *awserr.Error) {
	success, failure := destinations(d)
	var out []configuredDestination
	for _, side := range []struct {
		arn       string
		onFailure bool
	}{{success, false}, {failure, true}} {
		if side.arn == "" {
			continue
		}
		dest, err := destination.Parse(side.arn)
		_, bucket := dest.(destination.Bucket)
		queue, isQueue := dest.(destination.Queue)
		if err != nil || bucket && !side.onFailure || isQueue && strings.HasSuffix(queue.ARN.Resource, ".fifo") {
			return nil, errDestinationConfig(success, failure)
		}
		out = append(out, configuredDestination{arn: side.arn, dest: dest})
	}
	for _, d := range out {
		if err := s.checkDestination(ctx, c, fn, d); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// checkDestination refuses d when it is the function fn names or does not
// exist. A destination function is found in its own scope's state
// (lambda-api.md 7.7); a queue, topic or bucket is looked up in its own
// service (lookUpDestination). A bus is not: AWS accepts one that does not
// exist (aws-recorded, lambda-destinations-layers.md 2.3: LDL-08 03).
func (s *Service) checkDestination(ctx context.Context, c *kernel.Call, fn functionRef, d configuredDestination) *awserr.Error {
	scope, ok := destinationScope(fn, d.dest)
	if !ok {
		return errDestinationMissing(d.arn)
	}
	switch dest := d.dest.(type) {
	case destination.Function:
		_, err := view(s, scope, func(st *State) (struct{}, *awserr.Error) {
			return struct{}{}, st.checkFunctionDestination(fn, scope, d.arn, dest)
		})
		return err
	case destination.Queue:
		return lookUpDestination(ctx, s, c, d.arn, scope, sqsapi.OpGetQueueUrl,
			&sqsapi.GetQueueUrlInput{QueueName: dest.ARN.Resource, QueueOwnerAWSAccountId: new(dest.ARN.Account)}, sqsapi.IdentityQueueDoesNotExist(), errDestinationMissing(d.arn))
	case destination.Topic:
		return lookUpDestination(ctx, s, c, d.arn, scope, snsapi.OpGetTopicAttributes,
			&snsapi.GetTopicAttributesInput{TopicArn: dest.ARN.String()}, snsapi.IdentityNotFoundException(), errDestinationMissing(d.arn))
	case destination.Bus:
		return nil
	case destination.Bucket:
		return lookUpDestination(ctx, s, c, d.arn, scope, s3api.OpHeadBucket,
			&s3api.HeadBucketInput{Bucket: dest.Name}, s3api.IdentityNotFound(), errBucketDestinationMissing())
	}
	return awserr.Internalf(fmt.Errorf("lambda: destination %s parsed to %T, which checkDestination has no check for", d.arn, d.dest))
}

// destinationScope is the account and region d is found in: its ARN's, or
// the function's for a bucket, whose ARN names neither. False when the
// ARN's partition, account or region is not one understudy can hold, so
// the destination cannot exist.
func destinationScope(fn functionRef, d destination.Destination) (aws.Scope, bool) {
	var a aws.ARN
	switch d := d.(type) {
	case destination.Queue:
		a = d.ARN
	case destination.Topic:
		a = d.ARN
	case destination.Function:
		a = d.ARN
	case destination.Bus:
		a = d.ARN
	case destination.Bucket:
		return fn.scope, true
	}
	scope, err := aws.ScopeOf(a)
	return scope, err == nil
}

// checkFunctionDestination refuses dest, named arn and held in scope, when
// it is the function fn names, whatever its qualifier, or when st, scope's
// state, holds no function of its name (lambda-api.md 7.7). Its qualifier
// is not looked up.
func (st *State) checkFunctionDestination(fn functionRef, scope aws.Scope, arn string, dest destination.Function) *awserr.Error {
	name := strings.TrimPrefix(dest.ARN.Resource, "function:")
	if scope == fn.scope && name == fn.name {
		return errDestinationSelf()
	}
	if _, ok := st.Functions[name]; !ok {
		return errDestinationMissing(arn)
	}
	return nil
}

// recheckDestination repeats, in the write that stores the configuration,
// the check of a destination function in fn's own scope, which st holds
// and which may have been deleted since checkDestination. Every other
// destination is another scope's or service's state, which the write
// cannot read.
func (st *State) recheckDestination(fn functionRef, d configuredDestination) *awserr.Error {
	dest, ok := d.dest.(destination.Function)
	if !ok {
		return nil
	}
	if scope, ok := destinationScope(fn, dest); ok && scope == fn.scope {
		return st.checkFunctionDestination(fn, scope, d.arn, dest)
	}
	return nil
}

// lookUpDestination asks the service of the destination arn whether it
// exists in scope, as Lambda on its own behalf (kernel.AsPlatform) over
// the destination mechanism: the execution role's permission is not
// checked at configuration (lambda-destinations-layers.md 11.1). AWS
// looks up queues, topics and buckets (aws-recorded, 2.3: LDL-08; switch
// lambda.destination.config-existence-check). Only that service's
// not-found answer means the destination is missing, which is refused
// with missing; any other failure leaves its existence unknown, so the
// call fails with InternalFailure and nothing is stored.
func lookUpDestination[In, Out any](ctx context.Context, s *Service, c *kernel.Call, arn string, scope aws.Scope, op kernel.Op[In, Out], in *In, notFound awserr.Identity, missing *awserr.Error) *awserr.Error {
	t := kernel.Target{Scope: scope, As: kernel.AsPlatform{Name: lambdaService}, Via: ref.LambdaDestination}
	_, err := kernel.Invoke(ctx, s.k, c, op, t, in)
	switch {
	case err == nil:
		return nil
	case err.ID.Shape == notFound.Shape:
		return missing
	case err.ID == awserr.RequestAbortedException():
		return err
	}
	var id ref.OperationID
	if d := op.Descriptor(); d != nil {
		id = d.ID
	}
	return awserr.Internalf(&DestinationCheckError{Destination: arn, Op: id, Err: err.AsError()})
}

// DestinationCheckError is a destination lookup that failed other than
// with the destination service's not-found answer, so Lambda cannot tell
// whether the destination exists and stored nothing.
type DestinationCheckError struct {
	Destination string
	Op          ref.OperationID
	// Err is the lookup's error; an emulator fault behind it unwraps.
	Err error
}

func (e *DestinationCheckError) Error() string {
	return fmt.Sprintf("lambda: looking up the destination %s with %s failed: %v; whether it exists is unknown, so the event-invoke configuration was not stored; retry the request",
		e.Destination, e.Op, e.Err)
}

func (e *DestinationCheckError) Unwrap() error { return e.Err }
