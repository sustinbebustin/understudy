package events

import (
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The message builders over the generated constructors. Texts are the
// recorded ones of secrets-logs-events-alarms.md 4 unless a comment says
// otherwise. ValidationException is not in the EventBridge model; the
// overlay's validationError identity is the code clients key on
// (tf-compute.md 4.3).

func errRuleNotFound(name string) *awserr.Error {
	return eventbridgeapi.ErrResourceNotFoundException("Rule " + name + " does not exist on EventBus " + defaultBus + ".")
}

func errBusNotFound(name string) *awserr.Error {
	return eventbridgeapi.ErrResourceNotFoundException("Event bus " + name + " does not exist.")
}

// errForeignBus is understudy's own answer to a control-plane call on
// another account's or region's default bus: that bus exists, so "does not
// exist" would mislead, and understudy does not manage it for this caller.
func errForeignBus(bus aws.ARN) *awserr.Error {
	return eventbridgeapi.ErrNotImplemented("understudy manages rules, targets and buses only on the caller's own default event bus, and " +
		bus.String() + " belongs to account " + bus.Account + " in " + bus.Region +
		". Call with that account's credentials in that region, or name the bus 'default'. " +
		"Run 'understudy coverage --service eventbridge' for what is supported.")
}

// errNoPatternOrSchedule carries moto's text; AWS's is unrecorded.
func errNoPatternOrSchedule() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Parameter(s) EventPattern or ScheduleExpression must be specified.")
}

func errInvalidPattern(message string) *awserr.Error {
	return eventbridgeapi.ErrInvalidEventPatternException(message)
}

func errRuleHasTargets() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Rule can't be deleted since it has targets.")
}

func errTooManyTargets() *awserr.Error {
	return eventbridgeapi.ErrLimitExceededException("The requested resource exceeds the maximum number allowed.")
}

// errTooManyRules is inferred: AWS answers a rule past the per-bus quota
// with LimitExceededException, and the recorded target-limit text is
// assumed to be shared.
func errTooManyRules() *awserr.Error {
	return eventbridgeapi.ErrLimitExceededException("The requested resource exceeds the maximum number allowed.")
}

func errInputsExclusive(targetID string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Only one of Input, InputPath, or InputTransformer must be provided for target " + targetID + ".")
}

func errInvalidPlaceholder(targetID, name string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("InputTemplate for target " + targetID + " contains invalid placeholder " + name + ".")
}

// errFIFONeedsGroup is inferred: AWS requires SqsParameters.MessageGroupId
// for a FIFO queue target, and its refusal text is unrecorded.
func errFIFONeedsGroup(targetID string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Parameter(s) SqsParameters must be specified for target: " + targetID + ".")
}

// errInvalidParameter follows the recorded "Parameter ScheduleExpression is
// not valid." and "Parameter Event is not valid."; for the other
// parameters it names (DeadLetterConfig.Arn, Input, InputPath,
// InputPathsMap, NextToken, ResourceARN) the text is inferred.
func errInvalidParameter(name string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Parameter " + name + " is not valid.")
}

func errEntriesTooLarge() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Total size of the entries in the request is over the limit.")
}

// errBusTags is understudy's own answer: the default bus takes no tags here,
// and accepting them without storing them would fail silently.
func errBusTags() *awserr.Error {
	return eventbridgeapi.ErrNotImplemented("understudy keeps tags on rules only, not on event buses. Run 'understudy coverage --service eventbridge' for what is supported.")
}

// The PutEvents per-entry failures (secrets-logs-events-alarms.md 4.7):
// HTTP 200 with the code and message in the entry.
const (
	codeInvalidArgument = "InvalidArgument"
	codeMalformedDetail = "MalformedDetail"
	msgMalformedDetail  = "Detail is malformed."
)

// A customer event whose Source starts with reservedSourcePrefix fails with
// NotAuthorizedForSourceException: the aws. namespace is reserved for AWS
// services. The code is the model's PutEventsResultEntry ErrorCode and an
// AWS sample records it for source aws.sagemaker; the message is inferred
// from the model's documentation, as no recorded text exists.
const (
	reservedSourcePrefix       = "aws."
	codeNotAuthorizedForSource = "NotAuthorizedForSourceException"
	msgNotAuthorizedForSource  = "Not authorized for the source."
)

func msgRequired(param string) string {
	return "Parameter " + param + " is not valid. Reason: " + param + " is a required argument."
}

// msgTooLong is inferred from msgRequired's recorded form: AWS's text for
// a member over its documented length is unrecorded.
func msgTooLong(param string, limit int) string {
	return "Parameter " + param + " is not valid. Reason: " + param + " is longer than " + strconv.Itoa(limit) + " characters."
}

// errTooManyTags is inferred: AWS documents the 50-tag limit, and its
// refusal's code and text are unrecorded. n is the tag count the request
// would leave.
func errTooManyTags(n int) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Tags are not valid. Reason: a resource can have at most " + strconv.Itoa(maxTags) +
		" tags, and this request would leave " + strconv.Itoa(n) + ".")
}

// codeNotImplemented is understudy's own per-entry failure code, in
// PutTargets and PutEvents, for an entry it cannot act on faithfully.
const codeNotImplemented = "NotImplemented"

// msgForeignRegionBus is understudy's PutEvents per-entry failure for an
// event sent, by ARN, to the default bus of another region: that bus
// exists, so dropping the event as AWS drops one for a missing bus would
// fail silently, and AWS's answer is unrecorded.
func msgForeignRegionBus(scope aws.Scope, bus aws.ARN) string {
	return "understudy sends PutEvents entries to default event buses in the caller's region (" + scope.Region.String() +
		"), and " + bus.String() + " is in " + bus.Region + "; the event was not sent. " +
		"Call PutEvents in " + bus.Region + ", or run 'understudy coverage --service eventbridge' for what is supported."
}

// msgUnsupportedTarget and msgUnsupportedParameters are understudy's own
// PutTargets per-entry failure for a target it cannot deliver to or whose
// parameters it does not model: storing it would make every delivery to it
// fail, or the parameters be ignored, silently.
func msgUnsupportedTarget(scope aws.Scope, arn string) string {
	return "understudy delivers EventBridge targets to Lambda functions, SQS queues and SNS topics in the rule's region (" + scope.Region.String() +
		"), in an account it serves, and " + arn + " is none of them; the target was not added. " +
		"Run 'understudy coverage --service eventbridge' for what is supported."
}

func msgUnsupportedParameters(members []string) string {
	return "understudy does not implement " + strings.Join(members, ", ") +
		" on EventBridge targets; the target was not added. Run 'understudy coverage --service eventbridge' for what is supported."
}
