// Package events implements Amazon EventBridge: the default bus and custom
// buses (docs/research/eventbridge-buses.md), rules with event patterns on
// any of them and schedule expressions on the default bus, PutEvents, the
// Lambda, SQS and SNS targets (docs/research/secrets-logs-events-alarms.md
// 4; ARCHITECTURE.md 16) and event bus targets (eventbridge-buses.md 6.2).
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
//     account stays the sender's, as a third-party recording against AWS
//     shows (eventbridge-buses.md 6.1). An event for another region's bus
//     fails its entry with NotImplemented, since AWS's answer is unrecorded
//     and dropping it would hide that it was not sent; a control-plane call
//     naming another account's or region's bus is NotImplemented, not
//     "does not exist".
//   - The internal PutEvents call of an events-to-events delivery puts the
//     one event an event bus target forwards, with the id, account, region
//     and time it was first put with (eventbridge-buses.md 10.4.12; keeping
//     the time is inferred, EBB-08). The PutEvents input has no member for
//     them, so they travel beside the call on its context, which only that
//     internal call reads. The event was accepted once, so its Source and
//     DetailType are not checked again and a scheduled aws.events event is
//     forwarded; its Detail must still parse. Such a call carries exactly
//     one entry; any other count is a bug in understudy and fails whole.
//   - A rule or target call on a custom bus that does not exist answers
//     "Event bus <name> does not exist.", the text of the bus operations;
//     AWS's answer is unrecorded (EBB-03). PutRule refuses a
//     ScheduleExpression on any bus but the default one before it parses
//     the expression or looks the bus up; the recording shows the refusal
//     for a missing bus, and the rest of the order is inferred.
//   - The bus choices no recording settles are the switches of
//     eventbridge-buses.md 10.5: DeleteEventBus refuses a bus with rules,
//     UpdateEventBus keeps an omitted member, a KMS key is stored unchecked,
//     ListEventBuses sorts by name, the default bus reports the scope's
//     first use as its creation time until a write (tags, UpdateEventBus,
//     PutPermission) stores it, removing a bus policy's last statement
//     removes the policy, and RemoveAllPermissions on a bus without one is
//     ResourceNotFoundException. A tag call changes no bus's
//     LastModifiedTime, which is inferred.
//   - A bus policy is stored and returned, never enforced
//     (eventbridge-buses.md 4.4, 10.7.22): PutPermission keeps its
//     statements in insertion order, upserted by Sid, each generated one in
//     the recorded member order, and a Policy document verbatim until a
//     statement call rebuilds the policy from its statements. The policy is
//     the bus's resource policy for the delivery access advisory and IAM
//     enforcement (ResourcePolicy). PutPermission refuses a Policy sent with
//     a statement member, a statement form missing a member, a Policy that
//     is not an object whose Statement is an object or a list of objects
//     with distinct string Sids, and a policy over 10,240 characters, in
//     that order, all before it looks the bus up except the last; the order
//     and the texts are inferred (EBB-06).
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
//     accounts, not regions), or an event bus in any served account and
//     region, and must not set a parameters member understudy does not
//     model (HttpParameters, KinesisParameters and the like); any other
//     fails its PutTargets entry with NotImplemented instead of being
//     stored and never delivered or acted on.
//   - An event bus target forwards each matched event as an
//     events-to-events delivery whose entry keeps the event's source,
//     detail-type, detail, account, region, time and resources; keeping the
//     time and resources is inferred (EBB-08). The id is new for a bus in
//     another region and kept otherwise, also for a bus of the rule's own
//     account and region (events.bus-target.same-account-event-id). The bus
//     need not exist when PutTargets adds it, and a delivery to a bus that
//     does not exist is dropped as PutEvents drops one; both are inferred
//     (EBB-08). The RoleArn is stored, not assumed.
//   - An event that arrived through an event bus target matches the
//     receiving bus's rules as usual and reaches their other targets, but
//     no bus target: each is journaled as a dropped delivery and not
//     dead-lettered (eb-cross-account.html; whether AWS leaves a trace is
//     unrecorded, EBB-08).
//   - Another account's bus target needs a RoleArn
//     (events.bus-target.cross-account-role) and takes no Input, InputPath
//     or InputTransformer, as AWS documents; both refusals fail PutTargets
//     with a ValidationException whose text is inferred (EBB-09). On a bus
//     of the rule's own account, AWS's handling of an input form is
//     unrecorded (EBB-08), so that entry fails with NotImplemented. A bus
//     target stored with an input form, which only an edited snapshot
//     holds, is journaled as a dropped delivery rather than forwarded with
//     its input ignored.
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
