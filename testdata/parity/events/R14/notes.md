# R14

- Date: 2026-10-14T07:03Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy built from /home/austin/dev/local-aws-stack (same corpus replayed on a local instance, account 000000000000; AWS account written as 123456789012).

- 01 invalid-expression error (status, __type, message): matches (400 ValidationException "Parameter ScheduleExpression is not valid.", internal/schedexpr/schedule.go:12)
- 01 stored ScheduleExpression (describe-rule): matches (stored verbatim, no normalisation, 01-NN-describe.out)
- 01-01..06, 01-08..14, 01-27 accept: matches
- 01-15, 01-16, 01-17, 01-20..25, 01-28, 01-31 reject: matches
- 01-07 cron(0 12 L-3 * ? *): differs: AWS accepts (01-07-put.out, 01-07-describe.out); understudy rejects, no L-n day-of-month form (internal/schedexpr/days.go:112-131)
- 01-18 cron(0 12 * * ? 1969), 01-19 cron(0 12 * * ? 2200): differs: AWS accepts both (01-18-*, 01-19-*); understudy rejects, year bounded 1970-2199 (internal/schedexpr/cron.go:10-13, 48)
- 01-26 cron(5/0 * * * ? *): differs: AWS accepts (01-26-*); understudy rejects increment 0 (internal/schedexpr/cron.go:96-99)
- 01-29 cron(0  12 * * ? *) (double space), 01-30 tab-separated: differs: AWS rejects 400 ValidationException "Parameter ScheduleExpression is not valid." (01-29-put.http, 01-30-put.http); understudy accepts runs of spaces and tabs (internal/schedexpr/cron.go:62; documented at internal/schedexpr/doc.go:26-28)
- 01 rate rows (01-32..45): matches (accepts 1 minute, 5 minutes, 1 hour, 1 day, 7 days; rejects the rest)
- 02 rate first fire: differs: AWS event times run creation + k*60s from k=0 (created 07:04:32.314Z, first event "time":"2026-10-14T07:04:34Z", delivered 07:05:09Z, 02-arrivals.jsonl); understudy first fires one interval after creation, time = creation + 60s (internal/service/events/rules.go:152 -> internal/schedexpr/schedule.go:110)
- 02 cron first fire (cron(0/2 * * * ? *)): matches (next matching minute, "time":"2026-10-14T07:06:00Z")
- 02 scheduled-event envelope (member order, detail-type "Scheduled Event", source aws.events, resources [rule ARN], detail {}): matches (internal/service/events/envelope/event.go:29-40, 49-)
- 02 Input constant target body {"c":1}: matches
- 02 InputTransformer body {"time":<t>,"id":<id>}: matches
- 02 event id format: differs (cosmetic): AWS ids are not RFC 4122 v4 (version nibble varies, e.g. "...-a2c4-...", 02-arrivals.jsonl, 03-*.out); understudy emits v4 UUIDs
- 02 delivery latency: n/a (AWS delivers 35-45 s after the event time; understudy delivers at the due instant on the emulator clock, by design)
- 02 while disabled: matches (no deliveries, 02-arrivals-while-disabled.jsonl empty)
- 02 enable restarts rate phase: matches in kind (AWS re-anchors at enable: enabled 07:11:21.433Z, next event time 07:11:22Z, phase moved from :34; understudy restarts at enable, internal/service/events/rules.go:280-298); differs in offset as in the first-fire row: AWS's first event after enable has time = enable instant (delivered 07:12:05Z), understudy's is enable + 60s
- 03 source aws.foo / aws.events: matches (NotAuthorizedForSourceException "Not authorized for the source.", now AWS-recorded; internal/service/events/errors.go:100-109)
- 03 missing DetailType: matches (InvalidArgument "Parameter DetailType is not valid. Reason: DetailType is a required argument.")
- 03 Detail not JSON: matches (MalformedDetail "Detail is malformed.")
- 03 Detail over 256 KiB: matches (accepted, FailedEntryCount 0; understudy limit 1 MiB, internal/service/events/putevents.go:24)
- 03 past Time: matches (accepted)
- 03 nonexistent EventBusName: matches (200, EventId, FailedEntryCount 0)
- 04 schedule rule on a custom bus: n/a (AWS 400 ValidationException "ScheduleExpression is supported only on the default event bus."; understudy does not implement custom buses: CreateEventBus is NotImplemented and PutRule answers ResourceNotFoundException "Event bus <name> does not exist.", internal/service/events/buses.go:87-97, 136-137)
- H1-01, H1-02 exists:false on empty/other detail: matches (true, true)
- H8-01..03 string vs number: matches (false x3)
- H9-01, H9-02 full-length masks: matches (InvalidEventPatternException "Event pattern is not valid. Reason: IPv4 mask bits must be < 32" / "IPv6 mask bits must be < 128", no location suffix)
- H9-03-a 0.0.0.0/0 vs 10.0.0.1: matches (true)
- H9-03-b 0.0.0.0/0 vs 2001:db8::1: differs: AWS true (H9-03-b.out, Ruler's mask-0 behaviour); understudy false (internal/ruler/matcher.go:254-260; choice documented at internal/ruler/doc.go:111-112)
- H14-01 {"exactly":"a"}: differs: AWS accepts, Result true (H14-01.out); understudy 400 InvalidEventPatternException "Event pattern is not valid. Reason: Unrecognized match type exactly" (internal/ruler/compile.go:609-611, profile.go:132-140)
- H14-02, H14-04 $or members sharing values: differs: AWS false, false (H14-02.out, H14-04.out: order-dependent, as executed Ruler); understudy true, true (documented semantics, internal/ruler/profile.go:141-147, doc.go:104-110)
- H14-03 swapped $or: matches (true)
- H10 all six rejected: matches (400 InvalidEventPatternException)
- H10 message location suffix: differs: AWS appends Jackson's location to every H10 reason, e.g. "...Reason: It must be an Array followed with $or.\n at [Source: (String)\"{\"detail\":{\"$or\":{\"m\":[\"x\"]}}}\"; line: 1, column: 19]" (H10-01.http); understudy omits it (internal/ruler/profile.go:375-377; doc.go:91-93 says EventBridge leaves it out)
- H10-01, H10-02 reason: matches apart from the suffix ("It must be an Array followed with $or." / "Only JSON object is allowed in array of $or relationship.")
- H10-03..06 reason: differs: AWS reports the plain-key (legacy RuleCompiler) error: "Unrecognized match type m" (03), "Value of numeric must be an array." (04), "Match expression name not found" (05), "Unrecognized match type a" (06); understudy reports the strict $or error: "There must have at least 2 Objects in $or relationship." (internal/ruler/compile.go:335), "numeric is Ruler reserved fieldName which cannot be used inside $or." (compile.go:273), "Empty objects are not allowed" (compile.go:280), "Only JSON object is allowed in array of $or relationship." (compile.go:324)
- H10 accepted-shape matching: n/a (AWS accepted none, so no H10-0N-a/b test-event-pattern steps)
- H11-01 (7), H11-02 (9) accepted, H11-03 (11) rejected with "Rule is too complex - try using fewer wildcard characters or fewer repeating character sequences after a wildcard character": matches (internal/ruler/profile.go:75, 326-328)
- H12-01 10x10x10 accepted, H12-03 nested accepted: matches
- H12-02 7x11x13 rejected: matches in status; differs in text: AWS "Event pattern is not valid. Reason: Or operator is producing over 1000 rule combinations" (H12-02-7x11x13.http); understudy "...Reason: The pattern has more than 1000 $or combinations" (internal/ruler/profile.go:298)

