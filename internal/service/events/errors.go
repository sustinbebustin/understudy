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

// errRuleNotFound quotes the rule's bus by name, the default bus included
// (eventbridge-buses.md 5.4).
func errRuleNotFound(rule, bus string) *awserr.Error {
	return eventbridgeapi.ErrResourceNotFoundException("Rule " + rule + " does not exist on EventBus " + bus + ".")
}

func errBusNotFound(name string) *awserr.Error {
	return eventbridgeapi.ErrResourceNotFoundException("Event bus " + name + " does not exist.")
}

// errBusExists is third-party-recorded (eventbridge-buses.md 3.1).
func errBusExists(name string) *awserr.Error {
	return eventbridgeapi.ErrResourceAlreadyExistsException("Event bus " + name + " already exists.")
}

// errDeleteDefaultBus is third-party-recorded (eventbridge-buses.md 3.5).
func errDeleteDefaultBus() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Cannot delete event bus " + defaultBus + ".")
}

// errReservedBusName and errSlashInBusName are inferred: the CreateEventBus
// page states both refusals, and AWS's texts are unrecorded
// (eventbridge-buses.md 2.2, EBB-02). Each is that page's sentence.
func errReservedBusName() *awserr.Error {
	return eventbridgeapi.ErrValidationException("You can't use the name " + defaultBus + " for a custom event bus.")
}

func errSlashInBusName() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Custom event bus names can't contain the / character.")
}

// errTooManyBuses is inferred: the model declares LimitExceededException for
// CreateEventBus, and the recorded target-limit text is assumed to be shared
// (eventbridge-buses.md 7, EBB-02).
func errTooManyBuses() *awserr.Error {
	return eventbridgeapi.ErrLimitExceededException("The requested resource exceeds the maximum number allowed.")
}

// errBusHasRules is inferred: DeleteEventBus requires that "All rules
// associated with this event bus need to be deleted", and AWS's answer to a
// bus that still has rules is unrecorded (eventbridge-buses.md 3.5, EBB-01;
// the events.delete-bus.with-rules switch).
func errBusHasRules(name string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Cannot delete event bus " + name + ". All rules associated with this event bus need to be deleted.")
}

// errPartnerBus is understudy's own answer to CreateEventBus with an
// EventSourceName: partner buses are out of scope, and a plain custom bus in
// its place would fake one (eventbridge-buses.md 10.7).
func errPartnerBus() *awserr.Error {
	return eventbridgeapi.ErrNotImplemented("understudy does not implement partner event buses, so CreateEventBus with EventSourceName created nothing. " +
		"Create a custom bus without EventSourceName, or run 'understudy coverage --service eventbridge' for what is supported.")
}

// errForeignBus is understudy's own answer to a control-plane call on
// another account's or region's bus: that bus may exist, so "does not
// exist" would mislead, and understudy does not manage it for this caller.
func errForeignBus(bus aws.ARN) *awserr.Error {
	return eventbridgeapi.ErrNotImplemented("understudy manages rules, targets and buses only on the caller's own event buses, and " +
		bus.String() + " belongs to account " + bus.Account + " in " + bus.Region +
		". Call with that account's credentials in that region. " +
		"Run 'understudy coverage --service eventbridge' for what is supported.")
}

// errScheduleOnCustomBus is AWS-recorded
// (testdata/parity/events/R14/04-custom-bus-schedule.json).
func errScheduleOnCustomBus() *awserr.Error {
	return eventbridgeapi.ErrValidationException("ScheduleExpression is supported only on the default event bus.")
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
// event sent, by ARN, to a bus of another region: that bus may exist, so
// dropping the event as AWS drops one for a missing bus would fail
// silently, and AWS's answer is unrecorded (eventbridge-buses.md 6.1,
// EBB-10).
func msgForeignRegionBus(scope aws.Scope, bus aws.ARN) string {
	return "understudy sends PutEvents entries to event buses in the caller's region (" + scope.Region.String() +
		"), and " + bus.String() + " is in " + bus.Region + "; the event was not sent. " +
		"Call PutEvents in " + bus.Region + ", or run 'understudy coverage --service eventbridge' for what is supported."
}

// msgUnsupportedTarget and msgUnsupportedParameters are understudy's own
// PutTargets per-entry failure for a target it cannot deliver to or whose
// parameters it does not model: storing it would make every delivery to it
// fail, or the parameters be ignored, silently.
func msgUnsupportedTarget(scope aws.Scope, arn string) string {
	return "understudy delivers EventBridge targets to Lambda functions, SQS queues and SNS topics in the rule's region (" + scope.Region.String() +
		") and to event buses in any region, in an account it serves, and " + arn + " is none of them; the target was not added. " +
		"Run 'understudy coverage --service eventbridge' for what is supported."
}

// errCrossAccountRole and errCrossAccountInput are inferred: AWS requires a
// RoleArn on every cross-account event bus target created after 2023-03-02
// (eb-cross-account.html; events.bus-target.cross-account-role) and takes no
// input form on one (API_PutTargets), and its refusal texts are unrecorded
// (eventbridge-buses.md 6.3, EBB-09). The first follows errFIFONeedsGroup's
// form, the second is the PutTargets page's sentence.
func errCrossAccountRole(targetID string) *awserr.Error {
	return eventbridgeapi.ErrValidationException("Parameter(s) RoleArn must be specified for target: " + targetID + ".")
}

func errCrossAccountInput() *awserr.Error {
	return eventbridgeapi.ErrValidationException("Input, InputPath, and InputTransformer are not available with PutTarget if the target is an event bus of a different AWS account.")
}

// msgBusTargetInput is understudy's own PutTargets per-entry failure for an
// input form on an event bus target of the rule's account: AWS's handling
// is unrecorded (eventbridge-buses.md 6.2, EBB-08), so understudy neither
// stores an input it might not apply as AWS does nor guesses a refusal.
const msgBusTargetInput = "understudy forwards the matched event to an event bus target in the rule's account and does not implement Input, InputPath or " +
	"InputTransformer on it, since AWS's handling is unrecorded; the target was not added. Remove the input member, or run " +
	"'understudy coverage --service eventbridge' for what is supported."

// msgSecondHop is the journal's reason for a bus target of a rule that
// matched an event which reached its bus through an event bus target: AWS
// sends such an event to no further bus (eb-cross-account.html,
// eventbridge-buses.md 6.5, 10.4.13).
const msgSecondHop = "the event reached this bus through an event bus target, and EventBridge does not forward it to another bus (eb-cross-account.html); it was not sent"

// msgStoredBusTargetInput is the journal's reason for an event bus target
// stored with an input form, which PutTargets never stores (msgBusTargetInput,
// errCrossAccountInput), so it comes from an edited snapshot.
const msgStoredBusTargetInput = "the event bus target has an Input, InputPath or InputTransformer, which understudy does not implement on an event bus target " +
	"and PutTargets does not store, so it came from an edited snapshot; the event was not sent. " +
	"Remove the target with RemoveTargets and add it again with PutTargets"

func msgUnsupportedParameters(members []string) string {
	return "understudy does not implement " + strings.Join(members, ", ") +
		" on EventBridge targets; the target was not added. Run 'understudy coverage --service eventbridge' for what is supported."
}
