// Package events implements Amazon EventBridge: the default bus, rules with
// schedule expressions and event patterns, PutEvents and the Lambda, SQS and
// SNS targets (docs/research/phase1/secrets-logs-events-alarms.md 4;
// ARCHITECTURE.md 16).
//
// A rule's schedule is a timer on the emulator clock (TimerSchedule, keyed by
// the rule ARN). Each fire sends the scheduled event, stamped with the due
// instant, to every target of the rule and arms the next fire, so advancing
// the clock an hour across rate(5 minutes) delivers twelve events accepted
// 5, 10, ... 60 minutes after the rule started. PutEvents matches each event
// on the default bus against every enabled rule's pattern with the Event
// Ruler EventBridge profile. Both paths hand the outbox one Deliver effect
// per target, in target creation order across rules, with the target's
// retry policy (default 24 hours and 185 retries) and dead-letter queue.
//
// Decisions the research leaves open:
//
//   - PutEvents fails a malformed entry alone, with HTTP 200, even when no
//     entry is valid: the recorded single-entry failures answer that way,
//     although the documentation says such a request fails.
//   - An event for a bus other than the caller's default bus gets an
//     EventId and is dropped, as AWS drops events to a missing bus. The
//     PutEvents call's journal entry names that bus: the journal has no
//     delivery mechanism for an event that never reached a rule.
//   - A matched target that receives nothing (an InputPath that selects
//     nothing, a transformer output AWS drops) is journaled as a dropped
//     delivery from the rule to the target. So is every target of a rule
//     whose pattern could not be evaluated against the event, such as an
//     event past the matcher's work bound.
//   - A scheduled fire of a rule with no targets draws no event id, so
//     it leaves a seeded run's later ids where they were.
//   - A target must be a Lambda function, SQS queue or SNS topic in a
//     served account and region, and must not set a parameters member
//     understudy does not model (HttpParameters, KinesisParameters and
//     the like); any other fails its PutTargets entry with NotImplemented
//     instead of being stored and never delivered or acted on.
//   - A FIFO queue target without SqsParameters.MessageGroupId, an Input
//     that is not JSON, an unsupported InputPath or InputPathsMap path, and
//     a DeadLetterConfig that is not an SQS queue fail PutTargets with a
//     ValidationException whose text is inferred.
//   - RemoveTargets ignores an Id the rule has no target for.
//   - TestEventPattern requires the members its API reference lists (id,
//     account, source, time, region, resources, detail-type); without one
//     the event is "Parameter Event is not valid.".
package events
