package lambda

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
)

// ldlSwitch is a switch of lambda-destinations-layers.md 11.2: its Ref the
// anchor of the section that grounds it, its Value, a constant the code
// implements, recorded on AWS by the recording record (that note's section
// 12). The replayer does not run the record's steps: Lambda's are REST
// requests, and the deliveries need a function under Docker.
func ldlSwitch(name, value, anchor, record string, ops ...string) fidelity.Switch[string] {
	return fidelity.Switch[string]{
		Name:    name,
		Value:   value,
		Label:   evidence.AWSRecorded,
		Ref:     "docs/research/lambda-destinations-layers.md#" + anchor,
		Record:  record,
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: "recorded in testdata/parity/lambda/" + record + "; the replayer skips Lambda's REST and Docker steps"},
	}
}

// asyncSwitches are the asynchronous invocation and destination behaviors
// lambda-destinations-layers.md 11.2 chose without AWS-recorded evidence,
// all now recorded (12, 2026-11-03). The queue and the destination
// package implement each Value.
func asyncSwitches() []fidelity.Switch[string] {
	return []fidelity.Switch[string]{
		// An alias or version without its own event-invoke configuration
		// gets the defaults, not $LATEST's (asyncConfigOf).
		ldlSwitch("lambda.async.qualifier-config", "invoked-qualifier-only", "24-which-configuration-an-invocation-uses", "LDL-01",
			"lambda.Invoke", "lambda.PutFunctionEventInvokeConfig"),
		// A queue, topic or bucket named as a destination is looked up when
		// it is configured, and a missing one refused; a bus is not
		// (checkDestination).
		ldlSwitch("lambda.destination.config-existence-check", "queue-topic-bucket", "23-validation-at-configuration", "LDL-08",
			"lambda.PutFunctionEventInvokeConfig", "lambda.UpdateFunctionEventInvokeConfig"),
		// A record's functionArn is the invoked qualifier's: :$LATEST for
		// the unqualified function, :<alias> or :<version> otherwise
		// (sendRecords).
		ldlSwitch("lambda.destination.record-function-arn", "invoked-qualifier", "32-fields", "LDL-01", "lambda.Invoke"),
		// Lambda makes one attempt at a destination and journals a failure
		// (destination.To's delivery.None).
		ldlSwitch("lambda.destination.delivery-retry", "none", "46-when-delivery-fails", "LDL-09", "lambda.Invoke"),
		// A record over SNS's limit is published with both payloads
		// dropped and a deliveryError after responseContext
		// (destination.To).
		ldlSwitch("lambda.destination.sns-oversize", "drop-payloads", "42-sns", "LDL-05", "lambda.Invoke"),
		// A destination function is invoked asynchronously
		// (destination.To).
		ldlSwitch("lambda.destination.lambda-invocation-type", "Event", "43-lambda", "LDL-06", "lambda.Invoke"),
		// An EventBridge record's resources are the bus, then the qualified
		// function (destination.To).
		ldlSwitch("lambda.destination.eventbridge-resources-order", "bus-then-function", "44-eventbridge", "LDL-10", "lambda.Invoke"),
	}
}
