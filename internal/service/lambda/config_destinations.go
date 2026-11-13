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
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/lambda/destination"
	"github.com/sustinbebustin/understudy/internal/store"
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
		_, err := store.View2(s.cells, scope, func(st *State) (struct{}, *awserr.Error) {
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

// checkDestinationRoles asks whether role, the execution role of the
// version the configuration is for, may write to each SNS or SQS
// destination: sns:Publish on a topic, sqs:SendMessage on a queue. Under
// iam.enforce=enforce a refusal is errDestinationRoleDenied; advise
// journals it and accepts, and off asks nothing (kernel.AuthorizeAs).
// AWS's answer for a topic and a queue is recorded
// (iam-enforcement-surfaces.md 8.5: IAM-ENF-27); whether it checks a
// function, bus or bucket is unrecorded, so they are not checked.
func checkDestinationRoles(ctx context.Context, c *kernel.Call, role string, dests []configuredDestination) *awserr.Error {
	for _, d := range dests {
		denied, err := roleDenial(ctx, c, role, d)
		if err != nil {
			return err
		}
		if denied != "" {
			return errDestinationRoleDenied(denied, d.arn)
		}
	}
	return nil
}

// roleDenial asks whether role, a function's execution role, may write to
// d: sns:Publish on a topic, sqs:SendMessage on a queue; a function, bus
// or bucket is not asked about. It returns the name of the action refused
// under iam.enforce=enforce, or "" when the role may, under advise, which
// journals a denial, and under off, which asks nothing
// (kernel.AuthorizeAs); err is any other failure to ask.
func roleDenial(ctx context.Context, c *kernel.Call, role string, d configuredDestination) (denied string, err *awserr.Error) {
	var action registry.IAMAction
	var resource aws.ARN
	switch dest := d.dest.(type) {
	case destination.Topic:
		action, resource = registry.IAMAction{Prefix: "sns", Name: "Publish"}, dest.ARN
	case destination.Queue:
		action, resource = registry.IAMAction{Prefix: "sqs", Name: "SendMessage"}, dest.ARN
	case destination.Function, destination.Bus, destination.Bucket:
		return "", nil
	}
	roleARN, perr := aws.ParseARN(role)
	if perr != nil {
		return "", awserr.Internalf(fmt.Errorf("lambda: the function's execution role %q is not an ARN, so its permission on %s cannot be checked: %w", role, d.arn, perr))
	}
	q := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: action, Resource: kernel.ResourceARN{ARN: resource}}}}
	if err := kernel.AuthorizeAs(ctx, c, kernel.AsExecutionRole{Role: roleARN}, q); err != nil {
		if _, refused := err.Origin.(awserr.Denial); refused {
			return action.Name, nil
		}
		return "", err
	}
	return "", nil
}

// checkDeadLetter checks the dead-letter target a CreateFunction or
// UpdateFunctionConfiguration of the function fn sets, before it is
// stored; role is the execution role the function will have. Each refusal
// is AWS's recorded answer (lambda-destinations-layers.md 5.1: LDL-08 09,
// 10, 11 and 14, recorded on UpdateFunctionConfiguration; CreateFunction's
// inferred to be the same), in this order:
//   - an ARN of a service other than SQS or SNS (an event bus was
//     recorded; any other ARN, and a value that is not one, is inferred);
//   - a FIFO queue, before the queue is looked up (inferred). A FIFO topic
//     is accepted, as for a destination (2.2; unrecorded);
//   - a queue or topic that does not exist, looked up in its own service as
//     a destination is (lookUpDestination). The topic's text is inferred
//     from the queue's;
//   - under iam.enforce=enforce, a role that may not send to the queue or
//     publish to the topic (roleDenial); advise journals the denial and
//     accepts, off asks nothing. The topic's text is inferred from the
//     queue's, and that existence is checked before permission is inferred,
//     as for a destination.
//
// The caller runs it after the execution role's trust check (inferred: no
// recording orders the two).
func (s *Service) checkDeadLetter(ctx context.Context, c *kernel.Call, fn functionRef, role, target string) *awserr.Error {
	a, perr := aws.ParseARN(target)
	if perr != nil || a.Service != "sqs" && a.Service != "sns" {
		return errDeadLetterService()
	}
	missing := errDeadLetterQueueMissing()
	if a.Service == "sns" {
		missing = errDeadLetterTopicMissing()
	}
	dest, err := destination.Parse(target)
	if err != nil {
		// An SQS or SNS ARN that names no queue or topic: none can exist.
		return missing
	}
	if queue, ok := dest.(destination.Queue); ok && strings.HasSuffix(queue.ARN.Resource, ".fifo") {
		return errDeadLetterFIFO()
	}
	scope, ok := destinationScope(fn, dest)
	if !ok {
		return missing
	}
	if aerr := s.lookUpDeadLetter(ctx, c, target, scope, dest, missing); aerr != nil {
		return aerr
	}
	denied, aerr := roleDenial(ctx, c, role, configuredDestination{arn: target, dest: dest})
	switch {
	case aerr != nil:
		return aerr
	case denied != "":
		return errDeadLetterRoleDenied(denied, strings.ToUpper(a.Service))
	}
	return nil
}

// lookUpDeadLetter asks the queue's or topic's service whether dest, the
// dead-letter target named target, exists in scope; missing when it does
// not.
func (s *Service) lookUpDeadLetter(ctx context.Context, c *kernel.Call, target string, scope aws.Scope, dest destination.Destination, missing *awserr.Error) *awserr.Error {
	switch dest := dest.(type) {
	case destination.Queue:
		return lookUpDestination(ctx, s, c, target, scope, sqsapi.OpGetQueueUrl,
			&sqsapi.GetQueueUrlInput{QueueName: dest.ARN.Resource, QueueOwnerAWSAccountId: new(dest.ARN.Account)}, sqsapi.IdentityQueueDoesNotExist(), missing)
	case destination.Topic:
		return lookUpDestination(ctx, s, c, target, scope, snsapi.OpGetTopicAttributes,
			&snsapi.GetTopicAttributesInput{TopicArn: dest.ARN.String()}, snsapi.IdentityNotFoundException(), missing)
	case destination.Function, destination.Bus, destination.Bucket:
		// checkDeadLetter passes only SQS and SNS ARNs.
	}
	return awserr.Internalf(fmt.Errorf("lambda: dead-letter target %s parsed to %T, which is neither a queue nor a topic", target, dest))
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
// the destination mechanism; the execution role's permission is asked
// separately (checkDestinationRoles). AWS
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

// DestinationCheckError is a destination or dead-letter target lookup that
// failed other than with the target service's not-found answer, so Lambda
// cannot tell whether the target exists and stored nothing.
type DestinationCheckError struct {
	Destination string
	Op          ref.OperationID
	// Err is the lookup's error; an emulator fault behind it unwraps.
	Err error
}

func (e *DestinationCheckError) Error() string {
	return fmt.Sprintf("lambda: looking up the destination %s with %s failed: %v; whether it exists is unknown, so the configuration naming it was not stored; retry the request",
		e.Destination, e.Op, e.Err)
}

func (e *DestinationCheckError) Unwrap() error { return e.Err }
