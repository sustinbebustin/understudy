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

// recorded is sw with its value recorded on AWS by its record, whose
// steps the replayer does not run: Lambda's are REST requests, and the
// deliveries need a function under Docker.
func recorded(sw fidelity.Switch[string]) fidelity.Switch[string] {
	sw.Label = evidence.AWSRecorded
	sw.Settles = fidelity.NoScenario{Reason: "recorded in testdata/parity/lambda/" + sw.Record + "; the replayer skips Lambda's REST and Docker steps"}
	return sw
}

// differs is sw whose record recorded what AWS does instead of its value,
// which understudy does not build yet: aws says what.
func differs(sw fidelity.Switch[string], aws string) fidelity.Switch[string] {
	sw.Settles = fidelity.NoScenario{Reason: "recorded in testdata/parity/lambda/" + sw.Record + ", which differs: " + aws}
	return sw
}

// asyncSwitches are the asynchronous invocation and destination behaviors
// lambda-destinations-layers.md 11.2 chose without AWS-recorded evidence,
// most now recorded (12, 2026-11-03). The queue and the destination
// package implement each Value.
func asyncSwitches() []fidelity.Switch[string] {
	return []fidelity.Switch[string]{
		// An alias or version without its own event-invoke configuration
		// gets the defaults, not $LATEST's (asyncConfigOf).
		recorded(ldlSwitch("lambda.async.qualifier-config", "invoked-qualifier-only", evidence.AWSRecorded, "24-which-configuration-an-invocation-uses", "LDL-01",
			"lambda.Invoke", "lambda.PutFunctionEventInvokeConfig")),
		// A queue, topic or bucket named as a destination is looked up when
		// it is configured, and a missing one refused; a bus is not
		// (checkDestination).
		recorded(ldlSwitch("lambda.destination.config-existence-check", "queue-topic-bucket", evidence.AWSRecorded, "23-validation-at-configuration", "LDL-08",
			"lambda.PutFunctionEventInvokeConfig", "lambda.UpdateFunctionEventInvokeConfig")),
		// A record's functionArn is the invoked qualifier's: :$LATEST for
		// the unqualified function, :<alias> or :<version> otherwise
		// (sendRecords).
		recorded(ldlSwitch("lambda.destination.record-function-arn", "invoked-qualifier", evidence.AWSRecorded, "32-fields", "LDL-01", "lambda.Invoke")),
		// Lambda makes one attempt at a destination and journals a failure
		// (destination.To's delivery.None).
		recorded(ldlSwitch("lambda.destination.delivery-retry", "none", evidence.AWSRecorded, "46-when-delivery-fails", "LDL-09", "lambda.Invoke")),
		// A record over SNS's limit is published whole and SNS refuses it,
		// rather than losing its payload (destination.To). AWS delivers it
		// with both payloads dropped and a deliveryError instead (LDL-05).
		differs(ldlSwitch("lambda.destination.sns-oversize", "delivery-failure", evidence.AWSDoc, "42-sns", "LDL-05", "lambda.Invoke"),
			"AWS delivers the record with both payloads dropped and a deliveryError object"),
		// A destination function is invoked asynchronously
		// (destination.To).
		recorded(ldlSwitch("lambda.destination.lambda-invocation-type", "Event", evidence.AWSRecorded, "43-lambda", "LDL-06", "lambda.Invoke")),
		// An EventBridge record's resources are the bus, then the qualified
		// function (destination.To).
		recorded(ldlSwitch("lambda.destination.eventbridge-resources-order", "bus-then-function", evidence.AWSRecorded, "44-eventbridge", "LDL-10", "lambda.Invoke")),
	}
}
