// Package events implements Amazon EventBridge: the default bus and custom
// buses (docs/research/eventbridge-buses.md), rules with schedule
// expressions and event patterns on the default bus, PutEvents and the
// Lambda, SQS and SNS targets (docs/research/secrets-logs-events-alarms.md
// 4; ARCHITECTURE.md 16).
//
// A rule's schedule is a timer on the emulator clock (TimerSchedule, keyed by
// the rule ARN). Each fire sends the scheduled event, stamped with the due
// instant, to every target of the rule and arms the next fire, so advancing
// the clock an hour across rate(5 minutes) delivers twelve events accepted
// 5, 10, ... 60 minutes after the rule started. PutEvents matches each event
// against every enabled pattern of its bus's rules with the Event Ruler
// EventBridge profile. Both paths hand the outbox one Deliver effect
// per target, in target creation order across rules, with the target's
// retry policy (default 24 hours and 185 retries) and dead-letter queue.
//
// Decisions the research leaves open:
//
//   - PutEvents fails a malformed entry alone, with HTTP 200, even when no
//     entry is valid: the recorded single-entry failures answer that way,
//     although the documentation says such a request fails.
//   - An event for a bus that does not exist gets an EventId and is
//     dropped, as AWS drops events to a missing bus. The PutEvents call's
//     journal entry names that bus: the journal has no delivery mechanism
//     for an event that never reached a rule.
//   - An event sent by ARN to a bus of another account in the caller's
//     region reaches that bus's rules, as AWS sends events across accounts
//     in a region (eb-cross-account.html). No IAM is enforced, so the
//     receiving bus's resource policy is not consulted. The event's
//     account stays the sender's, which is inferred: no recording shows a
//     cross-account envelope. An event for another region's bus fails its
//     entry with NotImplemented, since AWS's answer is unrecorded and
//     dropping it would hide that it was not sent; a control-plane call
//     naming another account's or region's bus is NotImplemented, not
//     "does not exist".
//   - A rule or target call naming one of the caller's custom buses answers
//     NotImplemented: rules live on the default bus only.
//   - The bus choices no recording settles are the switches of
//     eventbridge-buses.md 10.5: DeleteEventBus refuses a bus with rules,
//     UpdateEventBus keeps an omitted member, a KMS key is stored unchecked,
//     ListEventBuses sorts by name, and the default bus reports the scope's
//     first use as its creation time until a write (tags, UpdateEventBus)
//     stores it. A tag call changes no bus's LastModifiedTime, which is
//     inferred.
//   - A Detail integer outside the signed 64-bit range API_PutEvents
//     documents fails its entry with MalformedDetail; the code and text are
//     inferred (the events.put-events.detail-integer-range switch).
//   - A Lambda target ARN must name a function, with at most one qualifier,
//     each part non-empty; any other form fails its PutTargets entry with
//     NotImplemented, as an unsupported target does.
//   - A matched target that receives nothing (an InputPath that selects
//     nothing, a transformer output AWS drops) is journaled as a dropped
//     delivery from the rule to the target. So is every target of a rule
//     whose pattern could not be evaluated against the event, such as an
//     event past the matcher's work bound.
//   - A scheduled fire of a rule with no targets draws no event id, so
//     it leaves a seeded run's later ids where they were.
//   - A target must be a Lambda function, SQS queue or SNS topic in the
//     rule's region and a served account (AWS delivers to these across
//     accounts, not regions), and must not set a parameters member
//     understudy does not model (HttpParameters, KinesisParameters and
//     the like); any other fails its PutTargets entry with NotImplemented
//     instead of being stored and never delivered or acted on.
//   - A FIFO queue target without SqsParameters.MessageGroupId, an Input
//     that is not JSON, an unsupported InputPath or InputPathsMap path, and
//     a DeadLetterConfig that is not a standard SQS queue in the rule's
//     region fail PutTargets with a ValidationException whose text is
//     inferred.
//   - PutEvents fails an entry whose Source is in the reserved aws.
//     namespace with NotAuthorizedForSourceException; the code is the
//     model's and an AWS sample's, the message is inferred.
//   - DeleteRule of a rule that does not exist succeeds, as on AWS.
//   - RemoveTargets ignores an Id the rule has no target for.
//   - TestEventPattern requires id, account, source, time, region and
//     detail-type; without one the event is "Parameter Event is not
//     valid.". Its API reference also lists resources, but a recording
//     against AWS matches an event without it (localstack
//     test_events_patterns.snapshot.json, test_event_pattern_source).
//   - A PutEvents DetailType over 128 characters fails its entry with
//     InvalidArgument, and a rule past 50 tags fails PutRule or TagResource
//     with ValidationException; both limits are documented, their refusal
//     texts are inferred.
//   - A FIFO target's SqsParameters.MessageGroupId that is a JSON path
//     takes its group from the matched event. Where the path selects no
//     string, number or boolean, AWS's behavior is unrecorded; understudy
//     journals a dropped delivery instead of guessing a group.
package events
