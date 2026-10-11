package events

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
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

func msgRequired(param string) string {
	return "Parameter " + param + " is not valid. Reason: " + param + " is a required argument."
}

// codeUnsupportedTarget, msgUnsupportedTarget and msgUnsupportedParameters
// are understudy's own PutTargets per-entry failure for a target it cannot
// deliver to or whose parameters it does not model: storing it would make
// every delivery to it fail, or the parameters be ignored, silently.
const codeUnsupportedTarget = "NotImplemented"

func msgUnsupportedTarget(arn string) string {
	return "understudy delivers EventBridge targets to Lambda functions, SQS queues and SNS topics in an account and region it serves, and " + arn +
		" is none of them; the target was not added. Run 'understudy coverage --service eventbridge' for what is supported."
}

func msgUnsupportedParameters(members []string) string {
	return "understudy does not implement " + strings.Join(members, ", ") +
		" on EventBridge targets; the target was not added. Run 'understudy coverage --service eventbridge' for what is supported."
}
