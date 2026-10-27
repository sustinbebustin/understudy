package lambda

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// ldlSwitch is a switch of lambda-destinations-layers.md 11.2: its Ref the
// anchor of the section that grounds it, settled by the recording record,
// which that note's section 12 queues. Its Value is a constant the code
// implements, not an option.
func ldlSwitch(name, value string, label evidence.Label, anchor, record string, ops ...string) fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   label,
		Ref:     "docs/research/lambda-destinations-layers.md#" + anchor,
		Record:  record,
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: "unrecorded: " + record + " is queued in docs/research/lambda-destinations-layers.md 12"},
	}
}

// asyncSwitches are the asynchronous invocation and destination behaviors
// chosen without AWS-recorded evidence (lambda-destinations-layers.md
// 11.2). The queue and the destination package implement each Value.
func asyncSwitches() []fidelity.Switch[string] {
	return []fidelity.Switch[string]{
		// An alias or version without its own event-invoke configuration
		// gets the defaults, not $LATEST's (asyncConfigOf).
		ldlSwitch("lambda.async.qualifier-config", "invoked-qualifier-only", evidence.Inferred, "24-which-configuration-an-invocation-uses", "LDL-01",
			"lambda.Invoke", "lambda.PutFunctionEventInvokeConfig"),
		// A queue, topic, bus or bucket named as a destination is looked up
		// when it is configured, and a missing one is refused in the words
		// of a missing destination function (checkDestination).
		ldlSwitch("lambda.destination.config-existence-check", "all", evidence.Inferred, "23-validation-at-configuration", "LDL-08",
			"lambda.PutFunctionEventInvokeConfig", "lambda.UpdateFunctionEventInvokeConfig"),
		// A record's functionArn is the invoked qualifier's: :$LATEST for
		// the unqualified function, :<alias> or :<version> otherwise
		// (sendRecords).
		ldlSwitch("lambda.destination.record-function-arn", "invoked-qualifier", evidence.Inferred, "32-fields", "LDL-01", "lambda.Invoke"),
		// Lambda makes one attempt at a destination and journals a failure
		// (destination.To's delivery.None).
		ldlSwitch("lambda.destination.delivery-retry", "none", evidence.Inferred, "46-when-delivery-fails", "LDL-09", "lambda.Invoke"),
		// A record over SNS's limit is published whole and SNS refuses it,
		// rather than losing its payload (destination.To).
		ldlSwitch("lambda.destination.sns-oversize", "delivery-failure", evidence.AWSDoc, "42-sns", "LDL-05", "lambda.Invoke"),
		// A destination function is invoked asynchronously
		// (destination.To).
		ldlSwitch("lambda.destination.lambda-invocation-type", "Event", evidence.Inferred, "43-lambda", "LDL-06", "lambda.Invoke"),
		// An EventBridge record's resources are the bus, then the qualified
		// function (destination.To).
		ldlSwitch("lambda.destination.eventbridge-resources-order", "bus-then-function", evidence.ThirdPartyRecorded, "44-eventbridge", "LDL-10", "lambda.Invoke"),
	}
}
