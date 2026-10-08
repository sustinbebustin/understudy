# Gap G3: CloudWatch Logs filter pattern semantics

Full write-up: [logs-filter-patterns.md](logs-filter-patterns.md). No AWS call was made.

## Result

- The grammar is complete from AWS documentation: unstructured terms (`?`, `-`, phrases), JSON patterns, space-delimited patterns and `%regex%`, as an EBNF with the event-side field tokenizer ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html), [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html)).
- A corpus of 167 (pattern, event, expected) vectors: 98 DOC, 4 REC, 65 INFERRED. A scratch reference matcher agrees with every row; 16 rows flip under the alternative rules for N1 and N6 and are named in the file.
- **Token boundary (Q1):** adopt case-sensitive substring, no word boundary, at about 60 percent confidence. The only direct AWS evidence is the TestMetricFilter example where `Exception` matches inside `InvalidQueryException`. No recorded test separates substring from whole-token matching, including LocalStack's dated pattern tests (2026-02-05) and the recorded unquoted `Token=` term (2026-01-14). The term test sits behind one function until case C1 is recorded.
- **Q11 `endTime`:** FilterLogEvents says "later than this time are not returned"; GetLogEvents says "equal to or later ... not included" ([FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html), [GetLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html)). Inclusive on both ends for FilterLogEvents, about 65 percent, no recording.
- **`limit` and pages:** `limit` only caps a page; the end signal is an absent `nextToken`. One recorded point: 25 events with `limit` 20 gave a full page of 20 plus a token ([LocalStack run, 2026-02-05](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.py)). understudy returns full pages and omits the token when nothing matching remains.
- **Space-delimited field count:** the DOC's own `[w1=ERROR, w2]` example only works if the last pattern field absorbs the remaining tokens. Working rule, LOW confidence, behind one function.
- moto marks no filter-pattern, time-range or paging test `aws_verified`; LocalStack's community matcher returns true for every pattern.

## Corrections made in other files

| File | Change | Source |
|---|---|---|
| `secrets-logs-events-alarms.md` 3.6 (token boundary bullet) | "Whole-token" suggestion withdrawn; substring working rule with confidence | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| `secrets-logs-events-alarms.md` 3.6 (regex bullet) | A literal space is also used in a DOC regex example | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| `secrets-logs-events-alarms.md` 3.6 (field-count bullet) | "More fields match only with `...`" replaced by the last-field-absorbs working rule | same |
| `secrets-logs-events-alarms.md` 3.6 (closing paragraph), 7 item 13, 8 (Q1 and Q11 rows) | "No AWS-REC test exists" qualified; corpus and grammar pointer; Q1 and Q11 narrowed | [LocalStack validation dates](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.validation.json) |

## Residual human recording

One script, ten cases, 199 queries, about 113 events, in section 9 of the full file. Run C1, C6, C8, C9 first; they decide the token rule, the field-count rule, `endTime` inclusivity and page-boundary tokens. It overlaps item R10 of `s3-checksum-residuals-and-record-backlog.md` and is a superset for patterns, time bounds and paging.

## Noticed, not changed

The CloudWatch Logs quota page lists 5 subscription filters per log group ([quotas](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/cloudwatch_limits_cwl.html)), and `secrets-logs-events-alarms.md` section 3.8 repeats that. A LocalStack run recorded on 2026-02-06 refused the third filter with `LimitExceededException` "Resource limit exceeded." ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_subscription_filters.snapshot.json)). Subscription filters are outside phase 1, so the file was left alone.
