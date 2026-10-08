# Gap G2: pattern-matching semantics from the Event Ruler source

Status: closed except the items that need a human AWS recording. Full write-up: [matching-engines.md](matching-engines.md).

## Result

- Source: [aws/event-ruler](https://github.com/aws/event-ruler) at `ced786201c1f43ec73482b78f3d1843aa4bd2f89` (v2.2.0 plus two commits), built and executed; tags v1.1.0 to v2.2.0 rebuilt to see version drift.
- EventBridge is Ruler ([AWS announcement 2024-02-01](https://aws.amazon.com/about-aws/whats-new/2024/02/amazon-eventbridge-event-bus-matching-event-ruler)); Ruler v2.2.0 passes 154 of 155 AWS-validated EventBridge fixtures (the miss is the wildcard-complexity cap).
- SNS: no AWS statement; 35 of 37 AWS-recorded policy errors are byte-identical to Ruler's, the other two come from an SNS operator allow-list. Lambda ESM: "same syntax as EventBridge" only; engine unproven, and its numeric-text note contradicts Ruler.
- Resolved from source: `anything-but` over arrays is any-element; `exists:true` matches `null` and `""` in Ruler; empty array/object means absent; numeric rules never match strings; unrepresentable numbers are rejected (`Cannot compare number`); `cidr` rejects `/32` and `/128`; Ruler has no key-count or combination cap.
- 85 seed vectors, the three-service difference tables and the master table are in matching-engines.md sections 5, 6 and 9.

## Claims corrected in other files (in place, marked "G2 note" or "G2 correction")

- secrets-logs-events-alarms.md 4.4: EventBridge docs do not cite Ruler; the 1000 `$or` cap is an EventBridge API rule, not Ruler; `{"wildcard":""}` matches the empty string; `$or` "ordinary key" fallback is narrower than stated.
- sns.md: Ruler relationship evidenced; section 9 rows for `anything-but` arrays, `exists:false`, key counting, numeric range ("all finite numbers" was wrong), `anything-but` suffix; `$or` ordinary-key sentence; bare numbers.
- lambda-events.md: numeric-text note conflicts with Ruler; plain-string body pattern form is derived; "shared EventBridge engine" is an assumption.

## Open (needs a human-run AWS recording)

13 cases H1 to H13 with exact patterns and events: matching-engines.md section 8.
