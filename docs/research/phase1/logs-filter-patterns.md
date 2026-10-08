# CloudWatch Logs filter patterns: grammar, vectors and boundary rules

Gap G3 deliverable, 2026-10-08. No AWS call was made. Every AWS fact below comes from AWS documentation, AWS-owned code, or a third-party test that recorded real AWS behavior.

Tags:
- **DOC**: stated or demonstrated by AWS documentation, an AWS API reference or Smithy model, or AWS-owned sample code or tests. re:Post answers are not DOC: the pages do not identify their authors as AWS staff, so they count as non-primary corroboration.
- **REC**: observed against real AWS by a test: LocalStack `@markers.aws.validated` tests (date taken from the sibling `*.validation.json`), or an integration test that ran against AWS.
- **INFERRED**: derived here; the derivation is stated.
- **UNVERIFIED**: no source confirms it.
- Confidence: HIGH (DOC states it), MEDIUM (one direct source or consistent indirect sources), LOW (a choice among live alternatives).

## 1. Result

- **Grammar.** Complete for unstructured terms, JSON patterns, space-delimited patterns and `%regex%` (section 4). The same grammar serves FilterLogEvents, metric filters, subscription filters and Live Tail ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html)).
- **Corpus.** 167 vectors in section 8: 98 DOC, 4 REC, 65 INFERRED. A throwaway reference matcher (not committed) was run over all of them under the rules in section 3, so the table is internally consistent. 16 vectors flip under the alternative rules for N1 and N6; they are named in section 8.
- **Token boundary (Q1).** Best rule N1: a term is a case-sensitive substring test on the whole message, with no word boundary. Confidence **MEDIUM-LOW, about 60 percent**. The one AWS example that exercises the question favors substring; nothing recorded separates the candidates (section 5). The whole-token guess in `secrets-logs-events-alarms.md` section 3.6 and Q1 had no source and is withdrawn there.
- **"No AWS-REC test exists" needs a qualifier.** LocalStack carries dated `aws.validated` pattern tests and one more recorded run that uses an unquoted `=` term, but none of their assertions can tell the candidate rules apart (section 2.2).
- **Q11 `endTime`.** FilterLogEvents documents `endTime` as "later than this time are not returned", unlike GetLogEvents ("equal to or later ... not included"). Rule N8: inclusive on both ends for FilterLogEvents. MEDIUM, no recording (section 6).
- **`limit` and pages.** `limit` only caps a page; pages may be short or empty; absence of `nextToken` is the only end signal. One REC point: 25 events with `limit` 20 gave a full first page of 20 plus a token. Rule N9 (section 6).
- **Human recording.** Ten cases, one script, 199 queries (section 9).
- **Side finding, not edited.** The quota page lists 5 subscription filters per log group but a LocalStack-recorded run on 2026-02-06 refused the third (section 2.2). Subscription filters are outside phase 1.

## 2. Sources

### 2.1 AWS documentation and AWS-owned code

| Source | What it gives |
|---|---|
| [Filter pattern syntax](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) | All unstructured, JSON, space-delimited and regex examples; case sensitivity; `" "` match-all; quotas (1 wildcard selector per selector, 3 per compound pattern, 2 regex per JSON or delimited pattern, 5 regex patterns per group for metric and subscription filters). It links to nothing that defines word boundaries. |
| [2022 snapshot of the same page](https://github.com/awsdocs/amazon-cloudwatch-logs-user-guide/blob/f57cdaf401/doc_source/FilterAndPatternSyntax.md) (archived AWS repo, last commit before the content was deleted) | One example the current page dropped: `{ $.objectList[1].id = 2 }`. Describes `?ERROR ?ARGUMENTS` as matching ERROR or ARGUMENTS or both. |
| [Syntax for metric filters](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) | `{ $.eventType = "*" && $.sourceIPAddress != 123.123.* }`, `[ip, server, username, timestamp, request, status_code, bytes > 1000]`, `[w1=ERROR \|\| w1=%WARN%, w2]` ("ERROR or any superstring of WARN"), `{ $.latency = * } metricValue: $.latency`. |
| [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) | Seven request and response pairs with `matches[].eventNumber` and `extractedValues`: `[...]`, `[]`, `[..., size]`, `[..., status_code=200, size]`, `[..., request=*.html*, status_code=4*,]`, `"[ERROR]"`, `"[ERROR]" Exception`. Max 50 messages per call. Extraction keys are `$name` for named fields and `$<position>` for unnamed ones. |
| [FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html) and the [Smithy model](https://github.com/aws/api-models-aws/blob/main/models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json) | `startTime`/`endTime` wording, `limit`, paging, ordering, `startFromHead`, 24 hour token life, `filterPattern` 0 to 1024 characters. |
| [GetLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html) | The contrasting inclusive and exclusive wording. |
| Metric filter walkthroughs: [count log events](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountingLogEventsExample.html), [count a term](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountOccurrencesExample.html), [HTTP 404](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/Counting404Responses.html), [HTTP 4xx](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FindCountMetric.html), [Apache bytes](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/ExtractBytesExample.html) | `" "`, `Error` ("patterns are case-sensitive"), `[ip, id, user, timestamp, request, status_code=404, size]`, `status_code=4*`, `[ip, id, user, timestamp, request, status_code, size]` with `metricValue='$size'`. The 404 page adds that "the pattern needs to match with the entire log event message". |
| [VPC flow logs alarm](https://docs.aws.amazon.com/vpc/latest/userguide/process-records-cwl.html) and [record examples](https://docs.aws.amazon.com/vpc/latest/userguide/flow-logs-records-examples.html) | `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]` and real record lines. |
| [Security Hub CloudWatch controls](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html) | 14 CloudTrail JSON patterns, no spaces around operators, unquoted dotted values: `{($.eventSource=iam.amazonaws.com) && ...}`, `{($.errorCode="*UnauthorizedOperation") \|\| ($.errorCode="AccessDenied*")}`, `$.userIdentity.invokedBy NOT EXISTS`. |
| Subscription filters: [Kinesis, Lambda, Firehose](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/SubscriptionFilters.html), [PutSubscriptionFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutSubscriptionFilter.html), [PutAccountPolicy](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutAccountPolicy.html) | `{$.userIdentity.type = Root}`, `""`, `"Test"`, `ERROR`, `[ip, identity, user_id, timestamp, request, status_code = 500, size]`. Delivery format (`awslogs.data`, base64 gzip) is documented there and is out of scope for this file. |
| [What's New, regex support](https://aws.amazon.com/about-aws/whats-new/2023/09/amazon-cloudwatch-logs-regular-expression-filter-pattern-syntax-support) | `{ $.statusCode=%4[0-9]{2}% }`; the regex quotas. |
| [CloudWatch agent configuration](https://docs.aws.amazon.com/AmazonCloudWatch/latest/monitoring/CloudWatch-Agent-Configuration-File-Details.html) | The agent's `filters` array (`include` and `exclude` with an RE2 `expression`) is a different mechanism and not this grammar. Nothing here depends on it. |
| [aws-cdk `pattern.ts`](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/lib/pattern.ts) and [tests](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/test/pattern.test.ts) | AWS-owned generator: "never wrong to quote a string term, and required if the term contains non-alphanumerical characters", inner `"` escaped with a backslash, string operators limited to `=` and `!=`, column names `[a-zA-Z0-9_-]+`, at most one `...`, `&&` between restrictions on one column. |
| [aws-cdk integ tests](https://github.com/aws/aws-cdk/blob/main/packages/%40aws-cdk-testing/framework-integ/test/aws-appsync/test/integ.appsync-logmetrics.ts), [aws-samples e2e test](https://github.com/aws-samples/aws-codepipeline-bitbucket-integration/blob/master/tests/integration/end-to-end/complete-e2e.test.js) | `{ $.fieldName = "myQuery" && $.fieldInError IS TRUE }`; FilterLogEvents with `{ $.response_code >= 200 }`; `"Webhook processed successfully"`. They show accepted syntax, not results. |
| re:Post answers (non-primary: the pages show no AWS-staff badge): [limits and "Invalid character(s) in term"](https://www.repost.aws/questions/QUauhBe_zRQp2VN0JPFVDPjA/why-i-do-hit-metric-resource-limit-exceeded), [wildcards in JSON strings](https://repost.aws/questions/QUXz7lkxnyRNKrDMxLVXfd1w/no-value-to-graph-for-a-metric-filter-which-should-match-hundreds-of-lines-based-on-pattern-test) | Console error wording for an unquoted `<INFO>`; `{$.log = "*GET /espace-client/selfcare/contract/*" && $.log = "* 200 *"}` with `*` and spaces inside quotes. |

Caution on weight: the AWS examples contain visible hand edits (the TestMetricFilter sample shows `"$size": "1534,"` and the event text "Unhanded exception"), so a documented result is strong but not a recording.

### 2.2 Recorded or validated tests

LocalStack: all files under [tests/aws/services/logs](https://github.com/localstack/localstack/tree/main/tests/aws/services/logs) mark tests `@markers.aws.validated`; the dates are the `last_validated_date` values in the sibling `*.validation.json`. The repo is archived (last push 2026-03-23). Only rows that bear on patterns, paging, time ranges or limits are listed.

| Test | Date (UTC) | What it proves |
|---|---|---|
| [`test_filter_log_events_pagination`](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.py) | 2026-02-05 18:50:35 ([validation](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.validation.json)) | 25 events, `limit=20`, no pattern: first page has exactly 20 and a `nextToken`; the second page brings the total to 25. REC. No snapshot. |
| `test_filter_log_events_limit_validation` | 2026-02-05 18:50:36 | `limit` 10001 gives `InvalidParameterException`, "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000" ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.snapshot.json)). |
| `test_filter_log_events_unknown_token` | 2026-02-05 18:50:36 | `InvalidParameterException` "The specified nextToken is invalid." (snapshot). The test is `@pytest.mark.skip` on LocalStack ("TODO Raise error"). |
| `test_filter_log_events_basic`, `_with_log_stream_names`, `_interleaved`, `_response_header` | 2026-02-05 18:50:23 to 18:50:31 | Pattern-free reads; `eventId`, `timestamp`, `message` present. |
| `TestFilterLogEventsPatterns`: `simple_word`, `multiple_words`, `quoted`, `json` | 2026-02-05 18:51:57, 18:52:01, 18:52:05, 18:52:08 | Carry the marker and validation dates; the class is `@pytest.mark.skip` ("filtering is only supported in pro"). Messages: `hello`, `world`, `hello world`, `goodbye world`, `hello cruela`, `goodbye cruel world`. Assertions only require every returned message to contain the pattern text, so they pass whether `"hello cruel"` returns `hello cruela` or nothing. **They cannot separate substring from token matching.** The JSON case shows only that `{$.message = "hello"}` against non-JSON text is accepted. |
| [`test_put_metric_filter_invalid_filter_pattern`](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_metric_filters.py) | 2026-02-05 21:31:40 ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_metric_filters.snapshot.json)) | 1025 characters: `InvalidParameterException`, "... at 'filterPattern' failed to satisfy constraint: Member must have length less than or equal to 1024". |
| `test_put_metric_filter_basic` (one space), `_json_pattern` (`{$.message = "test"}`), `_with_special_namespace` (`filterPattern`) | 2026-02-05 21:13:19, 21:15:47, 21:29:55 ([validation](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_metric_filters.validation.json)) | AWS accepts these patterns. |
| `test_metric_filters_publish_to_cloudwatch` | 2026-02-09 19:26:41 | Metrics appear for a one-space pattern and a JSON filter. `defaultValue` is 0, so presence of the metric does not show a match. |
| [`test_put_subscription_filter_update`](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_subscription_filters.py) | 2026-02-06 20:46:57 ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_subscription_filters.snapshot.json)) | The empty string, then `[]`, accepted; `filterPattern: "[]"` echoed. |
| `test_put_subscription_filter_limit_exceeded` | 2026-02-06 20:49:46 | The third filter on one group: `LimitExceededException` "Resource limit exceeded." The docstring says 2; the [quota page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/cloudwatch_limits_cwl.html) lists 5. |
| [`TestSNSSubscriptionCrudV2::test_confirm_subscription`](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.py) | 2026-01-14 09:45:12 ([validation](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.validation.json), [snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.snapshot.json)) | `filter_log_events(filterPattern="Token=")[0]` on a Lambda log group returned the one echoed event whose `SubscribeURL` ends `...&Token=<hex>`, and the token then confirmed the subscription. REC that an unquoted term with `=` is accepted and matched inside a long token. The test carries an unconditional `@pytest.mark.skip` at HEAD (added in the same commit, [d7217cca72](https://github.com/localstack/localstack/commit/d7217cca72)). |

Other recordings:
- [boto2 integration test](https://github.com/boto/boto/blob/develop/tests/integration/logs/test_layer1.py) (2014, written against AWS, no recorded artifact): `test_metric_filter('[ip, id, user, ..., status_code=500, size]', [... 200 1534, ... 500 5324])` returns exactly 1 match. REC (undated).
- LocalStack community does not implement patterns: [`get_pattern_matcher`](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/logs/provider.py) returns `lambda _pattern, _log_event: True`, and its [unit test](https://github.com/localstack/localstack/blob/main/tests/unit/services/logs/test_logs.py) says "expect to always be True until proper filter methods are available".

moto: [`setup.cfg`](https://github.com/getmoto/moto/blob/master/setup.cfg) defines `aws_verified`. In `tests/test_logs` it marks export tasks, ARN formats, `get_log_events` by ARN and CloudFormation ([test_logs.py](https://github.com/getmoto/moto/blob/master/tests/test_logs/test_logs.py)). **No filter-pattern, time-range or paging test carries it**, and moto has no dated validation files. Its matcher ([utils.py](https://github.com/getmoto/moto/blob/master/moto/logs/utils.py)) treats a leading letter as a space-split list of substrings, a leading quote as one quoted substring, and anything else as match-all; its [tests](https://github.com/getmoto/moto/blob/master/tests/test_logs/test_logs_filter_log_events.py) expect `"hello cruel"` to match `hello cruela`. Its [models.py](https://github.com/getmoto/moto/blob/master/moto/logs/models.py) skips events with `timestamp > end_time`, which is inclusive. These are emulator behavior, not AWS evidence.

Non-primary corroboration, used only where labeled: an [Experts Exchange thread](https://www.experts-exchange.com/questions/29282044/AWS-CloudWatch-metric-filter-patterns.html) (2025) reports the console's pattern test returning `... [412] Errorlog has been reinitialized.` for `?Error ?ERROR ?error ?Failed ?FAILED ?failed ?Failure ?FAILURE ?failure`.

## 3. Normative rules

| # | Rule | Evidence | Confidence |
|---|---|---|---|
| N1 | An unquoted or quoted term matches when its text occurs anywhere in the message as a case-sensitive substring. No tokenization, no word boundary. | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) (`Exception` matches inside `InvalidQueryException`), non-primary report above; "case sensitive" is [DOC](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html). Section 5. | MEDIUM-LOW (60 percent); case sensitivity HIGH |
| N2 | A term that is blank after trimming whitespace matches every event, and so does an empty or all-blank pattern. | `" "` returns all events ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html)); the CLI example `--filter-pattern " "` passes one space and "match[es] any log event content" ([count log events](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountingLogEventsExample.html)); "If not provided, all the events are matched" ([FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html)); AWS accepted the empty string and a single space (REC above). Whether the literal quoted `" "` and the one-space pattern are the same thing, and a blank term next to other terms, are UNVERIFIED (C2). | HIGH for the bare cases |
| N3 | Plain terms are ANDed. `-term` excludes. `?term` is optional: when any plain or `-` term is present every `?` term is ignored, otherwise at least one `?` term must match. An exclusion-only pattern matches every event lacking the term (INFERRED). | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): "If you combine "?" with other filter patterns, all question mark terms will be ignored", worked example `?ERROR ?ARGUMENTS REQUEST`. Trap: [aws-cdk](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/test/pattern.test.ts) generates `?"foo" "bar" ?"baz"` for "any of two groups", which under this rule means just `"bar"`. | HIGH (DOC); CDK form flagged |
| N4 | `%regex%` is an unanchored search, case-sensitive, over the message (unstructured), the field value (delimited) or the string node (JSON). `^` and `$` anchor to the start and end of that string. No flags exist. Allowed: alphanumerics, `: _ # = @ / ; , -`, a literal space (the DOC example `^shutting down` uses one), operators `^ $ ? [ ] { } \| \ * + .`, escapes `\d \D \s \S \w \W \xhh`. `(` and `)` are rejected, `!` and multi-byte characters are rejected. | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) (`%AUTHORIZED%` matches `UNAUTHORIZED REQUEST`; `%[hc]at$%` "only at the end"; `%10.10.0.1%` can match `10010,051`). The engine is not named; Go RE2 after charset validation is a close fit. `$` against a trailing newline and `^` on multi-line messages are UNVERIFIED (C4). | HIGH for the table; LOW for newlines |
| N5 | JSON patterns: the message must parse as JSON, otherwise no match. A selector reaches zero or more nodes; a condition holds if any reached node satisfies it. A node that is an object or array never satisfies a comparison. `=` and `!=` on strings are whole-string comparisons where `*` stands for any run of characters including none; numbers compare numerically (`1e+3` is allowed); `= *` means the field exists; `IS NULL`, `IS TRUE`, `IS FALSE` test the node; `NOT EXISTS` holds when no node is reached (a JSON null exists). `!=` on a missing field does not match (INFERRED). Precedence `()` > `&&` > `\|\|`. | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) (`$.arrayKey[*] = %val.{2}%`, "points to an array or object ... won't match", precedence, `IS`, `NOT EXISTS`, `IS NOT` and `EXISTS` unsupported). Typing questions (`= "1"` on a number, `= true`), prefix text before the JSON, non-object roots, missing-field `!=` are UNVERIFIED (C5). | HIGH for documented forms; LOW for typing |
| N6 | Space-delimited patterns: split the message on whitespace into fields; a `[...]` span or a `"..."` span is one field and its delimiters are dropped. `...` stands for any number of fields (once per pattern). A trailing comma declares one more unnamed field. `[]` matches every event. A field condition compares the whole field: `=` and `!=` with `*` wildcards, numeric `< > <= >=`, `%regex%`, joined by `&&` and `\|\|`. Unnamed fields are `$1`, `$2`, ... in extraction. **Field count (provisional, LOW):** an event with fewer fields than the pattern needs does not match; with more, the last pattern field absorbs the rest. | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) ("Characters between brackets and double quotation marks are considered single fields"; `[w1=ERROR, w2]` "returns log events where the first word ... is ERROR" for events of 6 to 7 words); [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) (`[..., size]`, `[]`, trailing comma, extraction names); [boto2 test](https://github.com/boto/boto/blob/develop/tests/integration/logs/test_layer1.py). The count rule conflicts with the 404 page ("needs to match with the entire log event message") and with CDK's "'...' may be specified once to match any number of columns"; it is chosen because the DOC's own `[w1=ERROR, w2]` example would otherwise never match. | HIGH for tokenization and conditions; LOW for count |
| N7 | The pattern kind is chosen by the first non-blank character: `{` JSON, `[` space-delimited, otherwise unstructured. An unquoted `[ERROR]` is therefore a one-field delimited pattern, not a term; quote it. A pattern that does not parse is rejected with `InvalidParameterException`. Length 0 to 1024. | Quote rule: `"[ERROR]"` in [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html). First-character dispatch is INFERRED from the three documented shapes. | MEDIUM |
| N8 | FilterLogEvents time range is `startTime <= timestamp <= endTime`, both optional, both inclusive. GetLogEvents is `[startTime, endTime)`. | Section 6. | MEDIUM (65 percent) |
| N9 | `limit` 1 to 10000 (default 10000) caps a page. understudy returns full pages, sets `nextToken` only when matching events remain after the page, and never returns an empty page with a token. | Section 6. | HIGH for the contract; N9's "no token on an exactly full last page" is LOW |
| N10 | Order is event timestamp, then ingestion time, then PutLogEvents request id; `startFromHead` default true; `false` needs `startTime` on or after 2024-01-01 UTC. | [FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html). Tie order across streams on the same timestamp and ingestion time is UNVERIFIED (C10). | HIGH |
| N11 | Inside a quoted phrase, `\"` is a quote and `\\` is a backslash. A phrase is matched like a term (N1), spaces included. | [aws-cdk `quoteTerm`](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/lib/pattern.ts). Whitespace normalization inside phrases is UNVERIFIED (C2). | MEDIUM |

## 4. Grammar

ISO 14977 style. `alnum` is `[A-Za-z0-9]`. The unstructured kind and the lexical forms shared by all three kinds come first.

```ebnf
filter-pattern   = ws , [ json-pattern | delimited-pattern | text-pattern ] , ws ;
                   (* kind chosen by the first non-blank character; blank or empty = match all *)
ws               = { " " | "\t" | "\r" | "\n" } ;
ws1              = ( " " | "\t" | "\r" | "\n" ) , ws ;

(* ---- A. unstructured ---- *)
text-pattern     = text-term , { ws1 , text-term } ;
text-term        = [ "-" | "?" ] , ( phrase | regex | bare-term ) ;
phrase           = '"' , { phrase-char | "\" , any-char } , '"' ;
phrase-char      = any-char - ( '"' | "\" ) ;
bare-term        = bare-char , { bare-char } ;
bare-char        = alnum | "_" | "=" | ":" | "#" | "@" | "/" | ";" | "," | "." ;
                   (* only alnum and "=" are evidenced; "<" ">" are rejected (re:Post answer, non-primary);
                      the rest is the regex symbol list reused as a guess: UNVERIFIED *)

(* ---- regex, shared ---- *)
regex            = "%" , regex-unit , { regex-unit } , "%" ;
regex-unit       = alnum | ":" | "_" | "#" | "=" | "@" | "/" | ";" | "," | "-" | " "
                 | "^" | "$" | "?" | "[" | "]" | "{" | "}" | "|" | "*" | "+" | "."
                 | regex-escape ;
regex-escape     = "\" , ( "d" | "D" | "s" | "S" | "w" | "W" | "x" , hex , hex
                         | "^" | "$" | "?" | "[" | "]" | "{" | "}" | "|" | "\" | "*" | "+" | "." ) ;
                   (* "(" and ")" are not units: reach them with \x28 and \x29.
                      At most 2 regex per JSON or delimited pattern. *)

(* ---- B. JSON ---- *)
json-pattern     = "{" , ws , json-or , ws , "}" ;
json-or          = json-and , { ws , "||" , ws , json-and } ;
json-and         = json-primary , { ws , "&&" , ws , json-primary } ;
json-primary     = "(" , ws , json-or , ws , ")" | json-condition ;
json-condition   = selector , ws , ( cmp-op , ws , json-value | is-test | exists-test ) ;
cmp-op           = "=" | "!=" | "<" | ">" | "<=" | ">=" ;
is-test          = "IS" , ws1 , ( "NULL" | "TRUE" | "FALSE" ) ;
exists-test      = "NOT" , ws1 , "EXISTS" ;
selector         = "$" , step , { step } ;
step             = "." , ( key | "*" ) | ".[" , "'" , { any-char - "'" } , "'" , "]"
                 | "[" , ( digit , { digit } | "*" ) , "]" ;
key              = ( alnum | "_" | "-" ) , { alnum | "_" | "-" } ;
json-value       = number | json-string | regex | "*" ;
number           = [ "+" | "-" ] , digit , { digit } , [ "." , digit , { digit } ]
                   , [ ( "e" | "E" ) , [ "+" | "-" ] , digit , { digit } ] ;
json-string      = phrase | json-bare ;
json-bare        = ( alnum | "_" | "." | "*" ) , { alnum | "_" | "." | "*" } ;
                   (* observed bare values: Root, 123.123.*, iam.amazonaws.com, AccessDenied*;
                      "-" ":" "/" bare is UNVERIFIED. <, >, <=, >= need a number; regex and
                      strings take only = and != (aws-cdk validateStringOperator). Double quotes inside
                      [...] are a syntax error. At most 1 wildcard step per selector, 3 per pattern. *)

(* ---- C. space-delimited ---- *)
delimited-pattern = "[" , ws , [ field , { ws , "," , ws , field } , [ ws , "," ] ] , ws , "]" ;
field            = "..." | name , [ ws , field-or ] ;
name             = ( alnum | "_" | "-" ) , { alnum | "_" | "-" } ;
field-or         = field-and , { ws , "||" , ws , field-and } ;
field-and        = field-test , { ws , "&&" , ws , field-test } ;
field-test       = [ name , ws ] , cmp-op , ws , field-value ;
field-value      = number | json-string | regex ;
                   (* the first test directly follows the field name, so "ip=%..%" and "request =*.html*"
                      both parse; later tests repeat the name: "status_code = 404 || status_code = 410".
                      At most one "..." per pattern. *)

(* ---- event side, space-delimited only ---- *)
event-fields     = ws , [ event-field , { ws1 , event-field } ] , ws ;
event-field      = "[" , { any-char - "]" } , "]" | '"' , { any-char - '"' } , '"'
                 | ( any-char - ( " " | "\t" | "\r" | "\n" ) ) , { any-char - ( " " | "\t" | "\r" | "\n" ) } ;
```

Parse-only corpus (valid per source; no documented event): `{ $.bandwidth > 75 }`, `{ $.latency < 50 }`, `{ $.refreshRate >= 60 }`, `{ $.responseTime <= 5 }`, `{ $.errorCode = 400}` (no space before `}`), `{ $.errorCode != 500 }`, `{ $.number[0] = 1e+3 }`, `{ $.number[0] != 1e-3 }` ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html)); `{ $.statusCode=%4[0-9]{2}% }` ([What's New](https://aws.amazon.com/about-aws/whats-new/2023/09/amazon-cloudwatch-logs-regular-expression-filter-pattern-syntax-support)); `{ $.response_code >= 200 }` ([aws-cdk integ](https://github.com/aws/aws-cdk/blob/main/packages/%40aws-cdk-testing/framework-integ/test/aws-ecs/test/fargate/integ.service-connect.ts)); `[ip, identity, user_id, timestamp, request, status_code = 500, size]` ([PutSubscriptionFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutSubscriptionFilter.html)); `{ $.log = "*GET /espace-client/selfcare/contract/*" && $.log = "* 200 *" }` ([AWS Support](https://repost.aws/questions/QUXz7lkxnyRNKrDMxLVXfd1w/no-value-to-graph-for-a-metric-filter-which-should-match-hundreds-of-lines-based-on-pattern-test)); `{ $.latency = "*" }`, `{ $.field = %value% }`, `[..., status_code = "4*" && status_code != 403, bytes]` ([aws-cdk tests](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/test/pattern.test.ts)); the 14 Security Hub patterns ([source](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html)); the empty string, one space, `[]`, `{$.message = "test"}`, `pattern`, `filterPattern` (accepted by AWS in dated LocalStack runs, section 2.2).

## 5. The token-boundary question

Question (Q1): does `ERROR` match `ERRORS`, `ERROR:`, `[ERROR]`, `MY_ERROR`, and what are the delimiters? The documentation never states a boundary rule. Two candidates:

- **S (adopted as N1):** substring, no boundary.
- **T:** the message is cut into tokens at delimiter characters and a term matches a contiguous run of whole tokens. The delimiter set would be every character outside `[A-Za-z0-9_]` (a guess; the earlier research proposed the same set).

| Evidence | Favors | Weight |
|---|---|---|
| [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): `"[ERROR]" Exception` against `02 May 2014 00:34:16,142 [ERROR] Unhanded exception: InvalidQueryException` is shown matching, and against the `[ERROR] Terminating the application` line is shown not matching. The only `Exception` with a capital E in the matching event is the suffix of `InvalidQueryException` (`exception:` is lowercase and terms are case sensitive). | S | The only direct AWS statement. Medium: the sample has hand-edit artifacts. |
| Non-primary: [console pattern test](https://www.experts-exchange.com/questions/29282044/AWS-CloudWatch-metric-filter-patterns.html) returned `[412] Errorlog has been reinitialized.` for a pattern list that includes `?Error`; the only candidate is the prefix of `Errorlog`. | S | Low: secondhand, not reproducible. |
| [Syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): `ERROR` matches `[ERROR 400] BAD REQUEST`. | both | None. Punctuation next to the term does not block a match under either rule. |
| REC `Token=` ([SNS test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.py)): matched inside `...&Token=<hex>`. | both | None. Under T the term `Token=` reduces to the token `Token`, which is present. It does show `=` is legal in an unquoted term. |
| `" "` is the documented match-all ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html)). Under pure S a one-space phrase would match only events containing a space; under T it has zero tokens and matches all. N2 special-cases it under S. | T | Low. |
| The regex section demonstrates `%AUTHORIZED%` matching inside `UNAUTHORIZED`, and the metric filter page calls `w1=%WARN%` "any superstring of WARN"; both frame regex as the way to get substring behavior. | T | Low: that is about fields and about regex, not plain terms. |
| LocalStack `TestFilterLogEventsPatterns` (2026-02-05) | neither | Assertions pass in both worlds (section 2.2). |
| moto matcher | S | Emulator, not AWS. |

Decision: adopt S with a stated 60 percent confidence, implemented behind one function `termMatches(term, message)` so that a recording flips it in one place. The corpus rows B01 to B13 carry the S answer and a note where T differs; case C1 in section 9 settles it with 25 patterns against 22 messages.

Answers to the Q1 list under S, with the T answer for comparison:

| Event for pattern `ERROR` | S | T (delimiters = non `[A-Za-z0-9_]`) | Status |
|---|---|---|---|
| `[ERROR]` | match | match | DOC (as `[ERROR 400]`) |
| `ERROR:` | match | match | INFERRED, same as above |
| `ERRORS` | match | no match | open, 60/40 |
| `MY_ERROR`, `MYERROR`, `xERROR` | match | no match | open, 60/40 |
| `error` | no match | no match | DOC (case sensitive) |

Token delimiters (S): none for unstructured terms. For space-delimited patterns the delimiters are whitespace plus the `[...]` and `"..."` grouping; whether tab and newline count as whitespace there is UNVERIFIED (C6).

## 6. FilterLogEvents time range, limit and pages

**Time range (Q11).** [FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html): `startTime` "Events with a timestamp before this time are not returned", `endTime` "Events with a timestamp later than this time are not returned". [GetLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_GetLogEvents.html): `startTime` "equal to this time or later ... are included", `endTime` "equal to or later than this time are not included". The two operations are worded differently in the same service: GetLogEvents spells out "equal to" on both bounds and FilterLogEvents does not. Read literally, FilterLogEvents includes events at exactly `endTime` and at exactly `startTime` (N8). Whether the wording difference is deliberate is UNVERIFIED. No recording covers either boundary: the LocalStack logs tests never pass `endTime` to FilterLogEvents or GetLogEvents, and moto marks none of its time-range tests `aws_verified`. moto itself uses `timestamp > end_time` for both operations, so it agrees on FilterLogEvents and disagrees with the GetLogEvents documentation. Case C8 settles it with events 1 ms apart. Related validation: `startTime` and `endTime` have minimum 0 ([DOC](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html)); the error text for `startTime > endTime` is UNVERIFIED (C8).

**limit and page boundary.**
- DOC ([FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html)): a page holds up to 1 MB or 10,000 events; it "might only be partially full, or even empty"; "Specifying the `limit` parameter only guarantees that a single page doesn't return more log events than the specified limit, but it might return fewer events than the limit"; "If the results don't include a `nextToken`, then pagination is finished"; the token expires after 24 hours; the direction is fixed by the first request.
- REC ([pagination test](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.py), 2026-02-05): 25 events, `limit` 20, one stream, no pattern: page one is exactly 20 events with a token; the next page with the same `limit` returns the remaining 5. Whether that last page carries a token is not recorded.
- Rule N9 for understudy: filter first, then cut `limit`; return full pages; the token is an opaque cursor over the sort key (N10) plus the direction; omit `nextToken` when no matching event remains after the page; reject a token that does not decode with `InvalidParameterException` "The specified nextToken is invalid." (REC); expire tokens after 24 hours on the emulator clock. The 1 MB cap is applied to the sum of message bytes; the AWS size accounting for FilterLogEvents is UNVERIFIED. A client that loops until `nextToken` is absent behaves identically whether AWS sends a token after an exactly full last page or not; case C9 records it.
- `limit` above 10000: REC error text in section 7.

## 7. Errors

| Condition | Code and text | Tag |
|---|---|---|
| `limit` 10001 | `InvalidParameterException` (HTTP 400), "1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000" | REC, [snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.snapshot.json) |
| `filterPattern` longer than 1024 | `InvalidParameterException`, "1 validation error detected: Value '...' at 'filterPattern' failed to satisfy constraint: Member must have length less than or equal to 1024" (recorded for PutMetricFilter; FilterLogEvents shares the constraint in the [Smithy model](https://github.com/aws/api-models-aws/blob/main/models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json)) | REC for PutMetricFilter, [snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_metric_filters.snapshot.json); INFERRED for FilterLogEvents |
| Invalid `nextToken` | `InvalidParameterException`, "The specified nextToken is invalid." | REC, same snapshot file as the first row |
| Unquoted term with an illegal character | `InvalidParameterException`; the console shows "Invalid character(s) in term <INFO>" for an unquoted `<INFO>`. The API message text and quoting of the term are UNVERIFIED. | INFERRED from a [re:Post answer](https://www.repost.aws/questions/QUauhBe_zRQp2VN0JPFVDPjA/why-i-do-hit-metric-resource-limit-exceeded) (non-primary); text UNVERIFIED |
| Syntax errors: double quotes inside `$.[...]`, `IS NOT`, `EXISTS`, `%x!%`, parentheses in a regex | `InvalidParameterException`; messages UNVERIFIED | DOC for the rejection ([syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html)) |
| More than 2 regex in a JSON or delimited pattern | rejected; error type and text UNVERIFIED (C7) | DOC for the limit |
| `logStreamNames` together with `logStreamNamePrefix`; `startFromHead=false` with `startTime` before 2024-01-01; unknown group | `InvalidParameterException`; `InvalidParameterException`; `ResourceNotFoundException` | DOC ([FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html)); texts UNVERIFIED |

## 8. Vector corpus

Conventions:
- One row per (pattern, event). "match" means FilterLogEvents returns the event; for a metric filter it means the metric increments.
- Events are fixture ids defined below the table, or literals. Pipes inside code spans are escaped as `\|`; unescape when generating tests.
- The expected column assumes N1 (substring) and N6 (last field absorbs the rest). Rows that depend on either say so. Under T the rows B01, B04, B05, B06, B08, B09, B10, B11, B12 and the DOC row T40 flip to "no match" and B13 flips to "match"; T40 and B09 are DOC rows, which is why section 5 rates T below S. Under an exact field-count rule D06, D09, D10, D12 and D38 flip to "no match".
- Row tag is the provenance of the expected result, not of the pattern.
- `error` means the call is rejected with `InvalidParameterException`.

| ID | Pattern | Event | Expected | Tag | Source and note |
|---|---|---|---|---|---|
| T01 | `ERROR` | `a1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T02 | `ERROR` | `a3` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T03 | `ERROR` | `a6` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T04 | `ERROR ARGUMENTS` | `a3` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T05 | `ERROR ARGUMENTS` | `a4` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T06 | `ERROR ARGUMENTS` | `a1` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T07 | `ERROR ARGUMENTS` | `a2` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T08 | `ARGUMENTS ERROR` | `a3` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): terms are order-independent |
| T09 | `?ERROR ?ARGUMENTS` | `a1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T10 | `?ERROR ?ARGUMENTS` | `a3` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T11 | `?ERROR ?ARGUMENTS` | `a2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T12 | `?ERROR ?ARGUMENTS` | `a6` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T13 | `?ERROR ?ARGUMENTS REQUEST` | `a7` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): optional terms ignored |
| T14 | `?ERROR ?ARGUMENTS REQUEST` | `a8` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T15 | `?ERROR ?ARGUMENTS REQUEST` | `a9` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T16 | `?ERROR ?ARGUMENTS REQUEST` | `a3` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): no REQUEST, so ? terms do not rescue it |
| T17 | `"INTERNAL SERVER ERROR"` | `a5` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T18 | `"INTERNAL SERVER ERROR"` | `[ERROR 500] INTERNAL ERROR SERVER` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): phrase is ordered |
| T19 | `ERROR -ARGUMENTS` | `a1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T20 | `ERROR -ARGUMENTS` | `a2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T21 | `ERROR -ARGUMENTS` | `a3` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T22 | `ERROR -ARGUMENTS` | `a4` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T23 | `?ERROR -ARGUMENTS` | `a6` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): ? ignored next to an exclusion, leaving -ARGUMENTS |
| T24 | `-ARGUMENTS` | `a1` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): exclusion alone |
| T25 | `-ARGUMENTS` | `a3` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T26 | `" "` | `a1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): documented match-everything |
| T27 | `" "` | `hello` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): no space in event; match-everything is not a substring test |
| T28 | (empty) | `hello` | match | DOC | [FilterLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_FilterLogEvents.html): empty or absent pattern matches all |
| T29 | `%AUTHORIZED%` | `a2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T30 | `%AUTHORIZED%` | `a6` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T31 | `%AUTHORIZED%` | `a1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T32 | `%^starting\|^initializing\|^shutting down%` | `starting up now` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T33 | `%^starting\|^initializing\|^shutting down%` | `skipping initializing x` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T34 | `%colou?r%` | `the colour red` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T35 | `%[hc]at$%` | `the hat` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T36 | `%[hc]at$%` | `hat trick` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T37 | `%10.10.0.1%` | `10010,051` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): unescaped dot matches any char |
| T38 | `%10\.10\.0\.1%` | `10010,051` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T39 | `%10\.10\.0\.1%` | `ip=10.10.0.1 ok` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| T40 | `"[ERROR]" Exception` | `b4` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): Exception inside InvalidQueryException, see section 5 |
| T41 | `"[ERROR]" Exception` | `b5` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| T42 | `"[ERROR]" Exception` | `b1` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| T43 | `"[ERROR]"` | `b4` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| T44 | `"[ERROR]"` | `b5` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| T45 | `"[ERROR]"` | `b1` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| T46 | `Error` | `This message contains an Error` | match | DOC | [count-term example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountOccurrencesExample.html) |
| T47 | `Error` | `Test event 1` | no match | DOC | [count-term example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountOccurrencesExample.html) |
| T48 | `Error` | `an error occurred` | no match | DOC | [count-term example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountOccurrencesExample.html): case-sensitive |
| T49 | `ERROR` | `error: x` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): case-sensitive |
| T50 | `Token=` | `tok` | match | REC | [LocalStack SNS test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.py): unquoted '=' accepted; matched inside a long URL; does not separate N1 from T; recorded 2026-01-14 |
| T51 | `<INFO>` | `<INFO> started` | error | INFERRED | [re:Post answer (non-primary)](https://www.repost.aws/questions/QUauhBe_zRQp2VN0JPFVDPjA/why-i-do-hit-metric-resource-limit-exceeded): console wording 'Invalid character(s) in term' |
| T52 | `"<INFO>"` | `<INFO> started` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): quote terms with non-alphanumerics |
| T53 | `"say \"hi\""` | `they say "hi" loudly` | match | INFERRED | [aws-cdk pattern.ts](https://github.com/aws/aws-cdk/blob/main/packages/aws-cdk-lib/aws-logs/lib/pattern.ts): backslash escapes inside quotes (CDK quoteTerm) |
| T54 | `"Webhook processed successfully"` | `INFO Webhook processed successfully id=7` | match | INFERRED | [aws-samples e2e test](https://github.com/aws-samples/aws-codepipeline-bitbucket-integration/blob/master/tests/integration/end-to-end/complete-e2e.test.js) |
| T55 | (one space) | `hello` | match | DOC | [count log events](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/CountingLogEventsExample.html): the CLI example passes one space to match any log event content; AWS accepted it (REC, section 2.2) |
| B01 | `ERROR` | `t1` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T says no match |
| B02 | `ERROR` | `t2` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): both models match |
| B03 | `ERROR` | `t3` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): [ERROR 400] is documented as a match for ERROR |
| B04 | `ERROR` | `t4` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T with _ as a word character says no match |
| B05 | `ERROR` | `t5` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T says no match |
| B06 | `ERROR` | `t6` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T says no match |
| B07 | `ERROR` | `t7` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): both models match |
| B08 | `Error` | `t8` | match | INFERRED | [Experts Exchange (non-primary)](https://www.experts-exchange.com/questions/29282044/AWS-CloudWatch-metric-filter-patterns.html): N1: substring; non-primary user report saw this match in the console |
| B09 | `Exception` | `b4` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: documented match on a suffix of InvalidQueryException |
| B10 | `ERROR` | `t9` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T says no match |
| B11 | `ERR` | `a1` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: prefix of ERROR; T says no match |
| B12 | `"INTERNAL SERVER"` | `t11` | match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring; T says no match |
| B13 | `"SERVER ERROR"` | `t12` | no match | INFERRED | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): N1: substring needs adjacency including punctuation; T says match |
| J01 | `{ $.eventType = "UpdateTrail" }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J02 | `{ $.eventType = "UpdateTrails" }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J03 | `{ $.eventType = "Update*" }` | `j1` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): * wildcard inside string |
| J04 | `{ $.eventType = "*Trail" }` | `j1` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J05 | `{ $.eventType = "*" }` | `j1` | match | DOC | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html): documented as 'contains the property' |
| J06 | `{ $.sourceIPAddress != 123.123.* }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J07 | `{ $.sourceIPAddress != 111.* }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J08 | `{ $.arrayKey[0] = "value" }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J09 | `{ $.arrayKey[1] = "value" }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J10 | `{ $.arrayKey[1] = "another value" }` | `j1` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J11 | `{ $.eventType = %Trail% }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J12 | `{ $.eventType = %^Trail% }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): anchored regex |
| J13 | `{ $.arrayKey[*] = %val.{2}% }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J14 | `{ $.* = %111\.111\.111\.1[0-9]{1,2}% }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J15 | `{ $.['cluster.name'] = "c" }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J16 | `{ $.["cluster.name"] = "c" }` | `j1` | error | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): double quotes in bracket notation are a syntax error |
| J17 | `{ $.SomeObject IS NULL }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J18 | `{ $.eventType IS NULL }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J19 | `{ $.SomeOtherObject NOT EXISTS }` | `j1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J20 | `{ $.eventType NOT EXISTS }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J21 | `{ $.SomeObject NOT EXISTS }` | `j1` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): a null value exists |
| J22 | `{ $.objectList[1].id = 2 }` | `j1` | match | DOC | [2022 syntax page](https://github.com/awsdocs/amazon-cloudwatch-logs-user-guide/blob/f57cdaf401/doc_source/FilterAndPatternSyntax.md) |
| J23 | `{ $.objectList = "a" }` | `j1` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): selector points at an array |
| J24 | `{ $.objectList[0].name = "a" && $.objectList[1].id > 1 }` | `j1` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J25 | `{ $.SomeObject IS NOT NULL }` | `j1` | error | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): IS NOT not supported |
| J26 | `{ $.eventType EXISTS }` | `j1` | error | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): EXISTS not supported |
| J27 | `{ ($.user.id = 1) && ($.users[0].email = "John.Doe@example.com") }` | `j2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J28 | `{ $.user.email = "John.Stiles@example.com" \|\| $.coordinates[0][1] = "nonmatch" && $.actions[2] = "nonmatch" }` | `j2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): && binds tighter than \|\| |
| J29 | `{ ($.user.email = "John.Stiles@example.com" \|\| $.coordinates[0][1] = "nonmatch") && $.actions[2] = "nonmatch" }` | `j2` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J30 | `{ ($.user.id = 2 && $.users[0].email = "nonmatch") \|\| $.actions[2] = "GET" }` | `j2` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J31 | `{ $.coordinates[0][1] = 1 }` | `j2` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J32 | `{ $.user.id >= 1 }` | `j2` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J33 | `{ $.user.id > 1 }` | `j2` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J34 | `{ $.user.id != 1 }` | `j2` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J35 | `{ $.latency = * }` | `j3` | match | DOC | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) |
| J36 | `{ $.latency > 75 }` | `j3` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J37 | `{ $.latency <= 50 }` | `j3` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J38 | `{ $.latency = 5e1 }` | `j3` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): scientific notation is numeric |
| J39 | `{ $.missing = * }` | `j3` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| J40 | `{ $.eventType = "*" && $.sourceIPAddress != 123.123.* }` | `j1` | match | DOC | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) |
| J41 | `{ $.userIdentity.type = Root }` | `j7` | match | INFERRED | [subscription doc](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/SubscriptionFilters.html): pattern is documented; bare string value |
| J42 | `{($.errorCode="*UnauthorizedOperation") \|\| ($.errorCode="AccessDenied*")}` | `j4` | match | INFERRED | [Security Hub doc](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html) |
| J43 | `{($.errorCode="*UnauthorizedOperation") \|\| ($.errorCode="AccessDenied*")}` | `j5` | match | INFERRED | [Security Hub doc](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html): * matches the empty string |
| J44 | `{($.errorCode="*UnauthorizedOperation") \|\| ($.errorCode="AccessDenied*")}` | `j6` | no match | INFERRED | [Security Hub doc](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html) |
| J45 | `{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType !="AwsServiceEvent"}` | `j7` | match | INFERRED | [Security Hub doc](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html) |
| J46 | `{$.userIdentity.type="Root" && $.userIdentity.invokedBy NOT EXISTS && $.eventType !="AwsServiceEvent"}` | `j8` | no match | INFERRED | [Security Hub doc](https://docs.aws.amazon.com/securityhub/latest/userguide/cloudwatch-controls.html) |
| J47 | `{ $.fieldName = "myQuery" && $.fieldInError IS TRUE }` | `j9` | match | INFERRED | [aws-cdk integ test](https://github.com/aws/aws-cdk/blob/main/packages/%40aws-cdk-testing/framework-integ/test/aws-appsync/test/integ.appsync-logmetrics.ts) |
| J48 | `{ $.fieldName = "myQuery" && $.fieldInError IS TRUE }` | `j10` | no match | INFERRED | [aws-cdk integ test](https://github.com/aws/aws-cdk/blob/main/packages/%40aws-cdk-testing/framework-integ/test/aws-appsync/test/integ.appsync-logmetrics.ts) |
| J49 | `{ $.a = 1 }` | `hello world` | no match | INFERRED | [LocalStack filter test](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_filter_events.py): non-JSON message never matches; AWS accepted such a call without error (weak REC) |
| J50 | `{ $.eventType = "UpdateTrail" }` | `xx` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): malformed JSON message |
| D01 | `[ip, user, username, timestamp, request, status_code, bytes]` | `s1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D02 | `[ip=%127\.0\.0\.[1-9]%, user, username, timestamp, request =*.html*, status_code = 4*, bytes]` | `s1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D03 | `[..., request =*.html*, status_code = 4*, bytes]` | `s1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D04 | `[ip, user, username, timestamp, request =*.html*, status_code = 404 \|\| status_code = 410, bytes]` | `s1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D05 | `[ip, user, username, timestamp, request =*.html*, status_code = 404 \|\| status_code = 410, bytes]` | `s2` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D06 | `[w1=ERROR, w2]` | `w4` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): depends on N6 field count; this is the documented intent |
| D07 | `[w1=ERROR, w2]` | `w3` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D08 | `[w1=ERROR, w2]` | `w1` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D09 | `[w1=ERROR \|\| w1=WARNING, w2]` | `w3` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): depends on N6 |
| D10 | `[w1=ERROR \|\| w1=WARNING, w2]` | `w4` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): depends on N6 |
| D11 | `[w1=ERROR \|\| w1=WARNING, w2]` | `w1` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D12 | `[w1!=ERROR && w1!=WARNING, w2]` | `w1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): depends on N6 |
| D13 | `[w1!=ERROR && w1!=WARNING, w2]` | `w3` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D14 | `[w1!=ERROR && w1!=WARNING, w2]` | `w4` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D15 | `[logLevel, date, time, method, url=%/service/resource/[0-9]+$%, response_time]` | `w1` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D16 | `[logLevel, date, time, method, url=%/service/resource/[0-9]+$%, response_time]` | `w2` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D17 | `[logLevel, date, time, method, url=%/service/resource/[0-9]+/part/[0-9]+$%, response_time]` | `w2` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D18 | `[logLevel, date, time, method, url=%/service/resource/[0-9]+/part/[0-9]+$%, response_time]` | `w1` | no match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html) |
| D19 | `[ip, id, user, timestamp, request, status_code=404, size]` | `c1` | match | DOC | [404 example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/Counting404Responses.html) |
| D20 | `[ip, id, user, timestamp, request, status_code=404, size]` | `c2` | no match | DOC | [404 example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/Counting404Responses.html) |
| D21 | `[ip, id, user, timestamp, request, status_code=4*, size]` | `h1` | match | DOC | [4xx example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FindCountMetric.html) |
| D22 | `[ip, id, user, timestamp, request, status_code=4*, size]` | `ap1` | no match | DOC | [4xx example](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FindCountMetric.html) |
| D23 | `[ip, identity, user_id, timestamp, request, status_code, size]` | `ap2` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D24 | `[..., size]` | `ap1` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D25 | `[]` | `ap1` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): empty field list matches any event |
| D26 | `[..., status_code=200, size]` | `ap1` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D27 | `[..., status_code=200, size]` | `ap2` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D28 | `[..., status_code=200, size]` | `ap3` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D29 | `[..., request=*.html*, status_code=4*,]` | `h1` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html): trailing comma declares an unnamed last field |
| D30 | `[..., request=*.html*, status_code=4*,]` | `h2` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D31 | `[..., request=*.html*, status_code=4*,]` | `h3` | no match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D32 | `[..., request=*.html*, status_code=4*,]` | `h4` | match | DOC | [TestMetricFilter](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_TestMetricFilter.html) |
| D33 | `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]` | `f1` | match | INFERRED | [VPC flow log doc](https://docs.aws.amazon.com/vpc/latest/userguide/process-records-cwl.html): synthetic REJECT line on port 22 |
| D34 | `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]` | `f2` | no match | INFERRED | [VPC flow log doc](https://docs.aws.amazon.com/vpc/latest/userguide/process-records-cwl.html): ACCEPT line from the VPC docs |
| D35 | `[version, account, eni, source, destination, srcport, destport="22", protocol="6", packets, bytes, windowstart, windowend, action="REJECT", flowlogstatus]` | `f3` | no match | INFERRED | [VPC flow log doc](https://docs.aws.amazon.com/vpc/latest/userguide/process-records-cwl.html): REJECT but port 3389 |
| D36 | `[ip, server, username, timestamp, request, status_code, bytes > 1000]` | `s1` | match | INFERRED | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) |
| D37 | `[ip, server, username, timestamp, request, status_code, bytes > 1000]` | `s3` | no match | INFERRED | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) |
| D38 | `[w1=ERROR \|\| w1=%WARN%, w2]` | `w3` | match | DOC | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html): regex = 'any superstring of WARN'; depends on N6 |
| D39 | `[w1=ERROR \|\| w1=%WARN%, w2]` | `w1` | no match | INFERRED | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html) |
| D40 | `[a, b, c]` | `x y` | no match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): fewer fields than the pattern |
| D41 | `[f1, f2, f3]` | `"GET /a b" [10/Oct 13:25] x` | match | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): quoted and bracketed spans are one field |
| D42 | `[w1=ERROR, w2]` | `ERRORS 09/25/2014 x` | no match | INFERRED | [metric-filter page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntaxForMetricFilters.html): field equality is whole-field, unlike unstructured terms |
| D43 | `[ip, id, user, ..., status_code=500, size]` | `127.0.0.1 - frank "GET /apache_pb.gif HTTP/1.0" 500 5324` | match | REC | [boto integ test](https://github.com/boto/boto/blob/develop/tests/integration/logs/test_layer1.py): ellipsis between named fields; boto2 integration test against AWS, 2014 |
| D44 | `[ip, id, user, ..., status_code=500, size]` | `127.0.0.1 - frank "GET /apache_pb.gif HTTP/1.0" 200 1534` | no match | REC | [boto integ test](https://github.com/boto/boto/blob/develop/tests/integration/logs/test_layer1.py): same test, the 200 event is not matched |
| P01 | `%something!%` | `x` | error | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): ! is not an allowed regex character |
| P02 | `%(a\|b)%` | `a` | error | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): parentheses are rejected |
| P03 | `%\x28%` | `a(b` | match | DOC | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): \xhh reaches rejected characters |
| P04 | `X` x 1025 | `x` | error | REC | [LocalStack metric-filter snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/logs/test_logs_metric_filters.snapshot.json): Member must have length less than or equal to 1024 (PutMetricFilter) |
| P05 | `{ $.a = 1` | `{}` | error | INFERRED | [syntax page](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html): unterminated JSON pattern |

Fixtures:

| Id | Message |
|---|---|
| `a1` to `a9` | `[ERROR 400] BAD REQUEST`, `[ERROR 401] UNAUTHORIZED REQUEST`, `[ERROR 419] MISSING ARGUMENTS`, `[ERROR 420] INVALID ARGUMENTS`, `[ERROR 500] INTERNAL SERVER ERROR`, `[SUCCESS 200] AUTHORIZED REQUEST`, `[INFO] REQUEST FAILED`, `[WARN] UNAUTHORIZED REQUEST`, `[ERROR] 400 BAD REQUEST` |
| `b1`, `b4`, `b5` | `02 May 2014 00:34:12,525 [INFO] Starting the application`; `02 May 2014 00:34:16,142 [ERROR] Unhanded exception: InvalidQueryException`; `02 May 2014 00:34:16,224 [ERROR] Terminating the application` |
| `j1` | `{"eventType": "UpdateTrail", "sourceIPAddress": "111.111.111.111", "arrayKey": ["value", "another value"], "objectList": [{"name": "a", "id": 1}, {"name": "b", "id": 2}], "SomeObject": null, "cluster.name": "c"}` |
| `j2` | `{"user": {"id": 1, "email": "John.Stiles@example.com"}, "users": [{"id": 2, "email": "John.Doe@example.com"}, {"id": 3, "email": "Jane.Doe@example.com"}], "actions": ["GET", "PUT", "DELETE"], "coordinates": [[0, 1, 2], [4, 5, 6], [7, 8, 9]]}` |
| `j3` | `{"latency": 50, "requestType": "GET"}` |
| `j4`, `j5`, `j6` | `{"errorCode": "Client.UnauthorizedOperation"}`, `{"errorCode": "AccessDenied"}`, `{"errorCode": "NoAccessDenied"}` |
| `j7`, `j8` | `{"userIdentity": {"type": "Root"}, "eventType": "AwsApiCall"}`, the same with `"invokedBy": "x"` added inside `userIdentity` |
| `j9`, `j10` | `{"fieldName": "myQuery", "fieldInError": true}` and `false` |
| `s1` | `127.0.0.1 Prod frank [10/Oct/2000:13:25:15 -0700] "GET /index.html HTTP/1.0" 404 1534` |
| `s2`, `s3` | `s1` with status 500; `s1` with bytes 500 |
| `ap1`, `ap2`, `ap3` | `127.0.0.1 - frank [10/Oct/2000:13:25:15 -0700] "GET /apache_pb.gif HTTP/1.0" 200 1534`; `... [10/Oct/2000:13:35:22 -0700] "GET /apache_pb.gif HTTP/1.0" 500 5324`; `... [10/Oct/2000:13:50:35 -0700] "GET /apache_pb.gif HTTP/1.0" 200 4355` |
| `h1` to `h4` | `127.0.0.1 - frank [...] "GET /index.html HTTP/1.0" 404 1534`; `"GET /about-us/index.html HTTP/1.0" 200 5324`; `"GET /apache_pb.gif HTTP/1.0" 404 4355`; `"GET /products/index.html HTTP/1.0" 400 1534` |
| `c1`, `c2` | `127.0.0.1 - bob [10/Oct/2000:13:55:36 -0700] "GET /apache_pb.gif HTTP/1.0" 404 2326`; same with `/apache_pb2.gif` and `200` |
| `w1` to `w4` | `INFO 09/25/2014 12:00:00 GET /service/resource/67 1200`; `INFO 09/25/2014 12:00:01 POST /service/resource/67/part/111 1310`; `WARNING 09/25/2014 12:00:02 Invalid user request`; `ERROR 09/25/2014 12:00:02 Failed to process request` |
| `f1`, `f2`, `f3` | `2 123456789010 eni-1235b8ca123456789 172.31.9.69 172.31.9.12 49761 22 6 20 4249 1418530010 1418530070 REJECT OK` (synthetic); the ACCEPT record `... 172.31.16.139 172.31.16.21 20641 22 6 ... ACCEPT OK` and the REJECT record `... 172.31.9.69 172.31.9.12 49761 3389 6 ... REJECT OK` from the [VPC examples](https://docs.aws.amazon.com/vpc/latest/userguide/flow-logs-records-examples.html) |
| `t1` to `t12` | `ERRORS`; `ERROR: disk full`; `[ERROR]`; `MY_ERROR`; `MYERROR`; `ERROR_CODE=5`; `ERROR-5`; `Errorlog has been reinitialized.`; `xERROR`; `ERROR`; `INTERNAL SERVERS`; `INTERNAL SERVER, ERROR` |
| `tok` | `{"body": "{\"SubscribeURL\" : \"https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=arn:aws:sns:us-east-1:123456789012:t&Token=2336412f37fb687f5d51e6e2\"}"}` |

## 9. Residual cases a human must record

Nothing here was run. The script below is for a person with a sandbox AWS account. It creates one log group `/understudy-g3/<epoch>`, one stream per case (`C1/s0`, ...), calls PutLogEvents then FilterLogEvents (GetLogEvents in C8), prints one JSON object per query, and deletes the group unless `--keep`. It needs `logs:CreateLogGroup`, `CreateLogStream`, `PutLogEvents`, `FilterLogEvents`, `GetLogEvents`, `DeleteLogGroup`. Cost is negligible (113 events). `--dry-run` prints every planned request without importing boto3 or touching the network and was checked that way. This file overlaps item R10 of `s3-checksum-residuals-and-record-backlog.md`; this script is a superset for patterns, time bounds and paging.

| Case | Settles | Decision rule |
|---|---|---|
| C1 | N1 and the delimiter set: 25 patterns against 22 messages (`ERRORS`, `MY_ERROR`, `xERROR`, `ERROR:`, `ERROR_CODE=5`, `ERROR-1`, `ERROR.x`, `a,ERROR,b`, `Errorlog ...`, `InvalidQueryException`, tab and newline cases) | `ERROR` returns `ERRORS` and `xERROR`: keep S. Returns neither but returns `ERROR:`, `a,ERROR,b`: switch to T and read the delimiter set off `MY_ERROR`, `ERROR_CODE=5`, `ERROR-1`, `ERROR.x`. Also records whether `[ERROR]` unquoted behaves as a one-field delimited pattern, and `ERROR*`, `*ROR`. |
| C2 | N2 and N11: phrase spacing, tab inside a phrase, `" "`, `"  "`, `" "` alone vs a blank next to other terms | Whether phrases collapse whitespace; whether blank terms always match. |
| C3 | N3: `?` with plain and `-` terms, exclusion alone, the CDK-shaped `?"A" "B" ?"C"`, duplicate and contradictory terms | Confirms "all `?` ignored" and exclusion-only semantics. |
| C4 | N4: anchors with trailing and embedded newlines, flags (`%x%i`, `%(?i)x%`), `\(`, escapes, empty regex, multiple regex terms in an unstructured pattern | Pins `$` on `foo\n`, multi-line `^`, whether a flag suffix exists, quota enforcement for unstructured patterns. |
| C5 | N5: number vs string typing, `true` vs `"true"`, `null`, `!=` on a missing field, `= *` on null/array/object, wildcards in the middle of strings, `$.a[*]`, duplicate keys, non-object roots, text before or after the JSON, case of keys, `$.[...]` keys, `$[0]`, `$` | Fills every UNVERIFIED in N5. |
| C6 | N6: field-count rule (`[w1=ERROR]`, `[w1=ERROR, w2]` against 1, 2, 3 and 6 tokens), tab-separated and padded events, unclosed brackets and quotes, nested brackets, unanchored vs full-field regex (`127.0.0.10`), numeric vs string compare (`0404`, `404.0`, `abc`), duplicate and digit-leading names, two ellipses, trailing comma | Replaces the LOW field-count rule. If `[w1=ERROR, w2]` matches only 2-token events, flip to the exact rule and note that the DOC example is misleading. |
| C7 | Error texts and status codes for 31 malformed patterns, including 1024 vs 1025 characters, 3 regex terms, `<INFO>` | Fills section 7's UNVERIFIED texts. |
| C8 | N8 and Q11: events 1 ms apart queried with every start/end combination, plus `startTime > endTime`, against FilterLogEvents and GetLogEvents | Inclusive `endTime` on FilterLogEvents: events at `endTime` are in the result. |
| C9 | N9: 7 events with `limit` 3, 7, 6, 1, 10000, with and without a pattern, `startFromHead=false`, and a bad token | Whether an exactly full last page carries a token; whether sparse matches produce short or empty pages; token behavior. |
| C10 | N10: identical timestamps in two streams | Tie order by stream, ingestion time, request id. |

```python
#!/usr/bin/env python3
"""Record CloudWatch Logs filter-pattern behavior (gap G3). Run by a human only.

One log group, one stream family per case, PutLogEvents then FilterLogEvents
(and GetLogEvents where noted). Prints one JSON object per query. Deletes the log
group at the end unless --keep. `--dry-run` prints the planned calls and never
imports boto3 or touches the network.
Needs: logs:CreateLogGroup CreateLogStream PutLogEvents FilterLogEvents GetLogEvents DeleteLogGroup.
"""
import argparse
import json
import sys
import time

ERR_EVENTS = [
    "ERROR", "ERRORS", "ERROR:", "[ERROR]", "MY_ERROR", "MYERROR", "ERROR_CODE=5", "ERROR-1",
    "ERROR.x", "xERROR", "error", "Error: x", "an ERROR occurred", "a,ERROR,b", "status=ERROR",
    "(ERROR)", "ERROR\tTAB", "ERROR\nNEXT", "Errorlog has been reinitialized.",
    "unhanded exception: InvalidQueryException", "Exception in thread main", "java.lang.NullPointerException",
]
JSON_EVENTS = [
    '{"a":1}', '{"a":"1"}', '{"a":1.5}', '{"a":true}', '{"a":"true"}', '{"a":null}', '{"a":{"b":1}}',
    '{"a":[1,2,3]}', '{"a":"x y"}', '{"a":"abc"}', '{"b":1}', '[1,2]', '"str"', '123',
    'prefix {"a":1}', '{"a":1} suffix', '{"a":1}\n', '{"a-b":1,"a b":2}', '{"A":1}', '{"a":1,"a":2}',
    '{"a":[{"b":1},{"b":2}]}', '{"a":"\xe9"}',
]
DELIM_EVENTS = [
    "ERROR", "ERROR x", "ERROR x y", "ERROR a b c d e f", "WARNING a b", "INFO a b", "ERRORS a b",
    "ERROR\ta\tb", "  ERROR   a   b", '"q r" [s t] u', '[unclosed a b', 'a "unclosed b', '[[x] y] z',
    "127.0.0.10 a b", "127.0.0.1 a b", "x 404 y", "x 0404 y", "x 4040 y", "x abc y", "x 404.0 y",
]

CASES = {
    "C1": {"why": "unquoted and quoted term boundaries (token rule)", "events": ERR_EVENTS, "patterns": [
        "ERROR", '"ERROR"', "Error", "Exception", "ERR", "ROR", "ERROR:", "ERROR_CODE", "ERROR_CODE=5", "ERROR-1",
        "ERROR.x", "status=ERROR", '"MY_ERROR"', '"[ERROR]"', "[ERROR]", "ERROR*", "*ROR", "?ERROR",
        '"ERROR]"', "(ERROR)", '"(ERROR)"', "a,ERROR", '"a,ERROR,b"', "Exception in", '"Exception in"']},
    "C2": {"why": "phrases, whitespace, match-all", "events": [
        "INTERNAL SERVER ERROR", "INTERNAL  SERVER ERROR", "INTERNAL\tSERVER ERROR", "INTERNAL SERVER, ERROR",
        "INTERNAL SERVERS ERROR", "xINTERNAL SERVER ERROR", "hello", "a b"], "patterns": [
        '"INTERNAL SERVER ERROR"', '"INTERNAL SERVER"', '"SERVER ERROR"', '"INTERNAL  SERVER"', '"  SERVER"',
        '" "', '"  "', " ", "  ", '""', '"\t"', "INTERNAL SERVER", "SERVER   ERROR"]},
    "C3": {"why": "? and - composition", "events": ["A", "B", "A B", "C", "A C", "B C", "A B C", "none"], "patterns": [
        "A", "-A", "A -B", "?A ?B", "?A -B", "A ?B", "-A -B", "?A", "?A ?B ?C", "A B", "B A", "A -A",
        '?"A B" ?C', '?"A" "B" ?"C"', "-A ?B ?C", "-A -A"]},
    "C4": {"why": "regex anchoring, newline, flags, alternation, escapes, counts", "events": [
        "ERROR", "xERRORx", "error", "ab", "a|b", "abc\n", "line1\nline2", "foo\n", "foo", "a.b", "axb", "(paren)",
        "tab\there", "\xe9", "AB", "10.10.0.1", "aa", "x"], "patterns": [
        "%ERROR%", "%^ERROR$%", "%^ERROR%", "%ERROR$%", "%^line2%", "%line1$%", "%foo$%", "%a.b%", "%a\\.b%",
        "%a|b%", "%\\x41B%", "%[a-z]+%", "%\\d%", "%\\s%", "%ERROR%i", "%(?i)error%", "%\xe9%", "%%", "%a{2}%",
        "%ab?%", "%\\(paren\\)%", "%\\x28paren\\x29%", "%ERROR% %x%", "%ERROR% -%x%", "%A% %B% %C%", "%\\w+%", "%\\W%"]},
    "C5": {"why": "JSON value typing, missing fields, roots, wildcards", "events": JSON_EVENTS, "patterns": [
        "{ $.a = 1 }", '{ $.a = "1" }', "{ $.a = true }", "{ $.a IS TRUE }", '{ $.a = "true" }', "{ $.a = null }",
        "{ $.a IS NULL }", "{ $.a != 1 }", "{ $.a != 5 }", "{ $.a = * }", '{ $.a = "*" }', "{ $.a NOT EXISTS }",
        "{ $.a.b = 1 }", "{ $.a[0] = 1 }", "{ $.a[*] = 2 }", "{ $.a[*].b = 2 }", "{ $.* = 1 }", '{ $.a = "x*" }',
        '{ $.a = "*y" }', '{ $.a = "x*y" }', '{ $.a = "a*c" }', "{ $.a > 1 }", "{ $.a >= 1.5 }", "{ $.a = 1.50 }",
        "{ $.a = 1e0 }", '{ $.a = "\xe9" }', "{ $.A = 1 }", "{ $.['a b'] = 2 }", "{ $.['a-b'] = 1 }", "{ $.a-b = 1 }",
        "{ $[0] = 1 }", "{ $ = 1 }", "{ $.a = %1% }", "{ $.a = %^a% }", "{ $.a = 1 && $.a = 2 }"]},
    "C6": {"why": "space-delimited field-count rule, grouping, tabs, regex anchoring, numeric compare", "events": DELIM_EVENTS,
           "patterns": [
        "[w1=ERROR, w2]", "[w1=ERROR]", "[w1=ERROR, w2, w3]", "[w1, w2]", "[w1, w2, w3]", "[w1=ERROR, ...]", "[..., w9]",
        "[...]", "[]", "[a, b, c]", "[a, b, c, ...]", "[a=ERROR, ..., z]", "[a=ERROR || a=WARNING, b]", "[a!=ERROR, b]",
        "[a=%127\\.0\\.0\\.[1-9]%, b, c]", "[a=%^127\\.0\\.0\\.[1-9]$%, b, c]", "[a, b=404, c]", "[a, b=4*, c]", "[a, b>400, c]",
        "[a, b>=404, c]", "[a, b<500, c]", "[a, b=404.0, c]", "[a, b=\"404\", c]", "[a, b=%404%, c]", "[a, b, c] ", " [a, b, c]",
        "[a, b, c,]", "[a, a, c]", "[1a, b]", "[a, ..., ...]"]},
    "C7": {"why": "invalid pattern error text and status", "events": ["x"], "patterns": [
        "{ $.n = }", "{ $.a = 1", "[a, b", "%a(b%", '"unterminated', "ERROR {", "<INFO>", "ERROR:", "a&b", "a|b", "a!b",
        "{ $.a IS NOT NULL }", "{ $.a EXISTS }", "[..., ...]", "{ ($.a=1 }", "%%", "%a% %b% %c%", "{ $.a = %a% && $.b = %b% && $.c = %c% }",
        "A" * 1024, "A" * 1025, "-", "?", "- A", "? A", "{}", "{ }", "[ ]", "{ $.a = 1 } B", "A { $.a = 1 }", "%A%B", "A%B%"]},
    "C8": {"why": "startTime/endTime inclusivity (FilterLogEvents vs GetLogEvents)", "events": [
        "e0", "e1", "e2"], "offsets": [0, 1, 2], "queries": [
        {"startTimeOffset": 0, "endTimeOffset": 2}, {"startTimeOffset": 0, "endTimeOffset": 1},
        {"startTimeOffset": 1, "endTimeOffset": 1}, {"startTimeOffset": 1}, {"endTimeOffset": 0}, {"endTimeOffset": 2},
        {"startTimeOffset": 2, "endTimeOffset": 1},
        {"api": "get", "startTimeOffset": 0, "endTimeOffset": 2, "startFromHead": True},
        {"api": "get", "startTimeOffset": 1, "endTimeOffset": 1, "startFromHead": True}]},
    "C9": {"why": "limit and page boundaries, empty pages, token behavior", "events": [
        "m0 hit", "m1", "m2 hit", "m3", "m4 hit", "m5", "m6 hit"], "offsets": [0, 10, 20, 30, 40, 50, 60],
           "queries": [
        {"limit": 3, "follow": True}, {"limit": 7, "follow": True}, {"limit": 6, "follow": True}, {"limit": 1, "follow": True},
        {"limit": 10000, "follow": True}, {"limit": 3, "filterPattern": "hit", "follow": True},
        {"limit": 4, "filterPattern": "hit", "follow": True}, {"limit": 2, "filterPattern": "nomatch", "follow": True},
        {"limit": 3, "startFromHead": False, "startTime": "recent", "follow": True},
        {"limit": 3, "filterPattern": "hit", "startFromHead": False, "startTime": "recent", "follow": True},
        {"limit": 3, "nextToken": "invalid-token"}]},
    "C10": {"why": "ordering ties across streams", "streams": 2, "events": ["tie-a", "tie-b"], "offsets": [0, 0],
            "queries": [{"limit": 100, "follow": True}, {"limit": 1, "follow": True}]},
}


def plan(case_id, case, base):
    n_streams = case.get("streams", 1)
    offsets = case.get("offsets") or [i * 10 for i in range(len(case["events"]))]
    puts = []
    for s in range(n_streams):
        puts.append({"logStreamName": "%s/s%d" % (case_id, s),
                     "logEvents": [{"timestamp": base + o, "message": m} for o, m in zip(offsets, case["events"])]})
    queries = case.get("queries") or [{"filterPattern": p} for p in case["patterns"]]
    return puts, queries


def to_request(q, base, group, case_id):
    q = dict(q)
    api = q.pop("api", "filter")
    follow = q.pop("follow", False)
    for k in ("startTime", "endTime"):
        if q.get(k) == "recent":
            q[k] = base - 1000
    if "startTimeOffset" in q:
        q["startTime"] = base + q.pop("startTimeOffset")
    if "endTimeOffset" in q:
        q["endTime"] = base + q.pop("endTimeOffset")
    if api == "filter":
        q.update(logGroupName=group, logStreamNamePrefix=case_id + "/")
    else:
        q.update(logGroupName=group, logStreamName=case_id + "/s0")
    return api, follow, q


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--region", default="us-east-1")
    ap.add_argument("--only", default="", help="comma-separated case ids such as C1,C8; default all")
    ap.add_argument("--keep", action="store_true")
    ap.add_argument("--dry-run", action="store_true")
    a = ap.parse_args()
    ids = [c for c in a.only.split(",") if c] or list(CASES)
    base = int(time.time() * 1000) - 3600 * 1000
    group = "/understudy-g3/%d" % int(time.time())
    logs = None
    if not a.dry_run:
        import boto3
        logs = boto3.client("logs", region_name=a.region)
        logs.create_log_group(logGroupName=group)
    print(json.dumps({"group": group, "base": base, "dryRun": a.dry_run}))
    try:
        for cid in ids:
            case = CASES[cid]
            puts, queries = plan(cid, case, base)
            for p in puts:
                if a.dry_run:
                    print(json.dumps({"case": cid, "putLogEvents": {"logGroupName": group, **p}}))
                    continue
                logs.create_log_stream(logGroupName=group, logStreamName=p["logStreamName"])
                logs.put_log_events(logGroupName=group, **p)
            if not a.dry_run:
                want = len(case["events"]) * len(puts)
                for _ in range(60):
                    got = logs.filter_log_events(logGroupName=group, logStreamNamePrefix=cid + "/", limit=100)
                    if len(got["events"]) >= want:
                        break
                    time.sleep(3)
            for q in queries:
                api, follow, req = to_request(q, base, group, cid)
                if a.dry_run:
                    print(json.dumps({"case": cid, "api": api, "request": req, "follow": follow}))
                    continue
                pages = []
                try:
                    while True:
                        resp = (logs.get_log_events if api == "get" else logs.filter_log_events)(**req)
                        tok = resp.get("nextToken") or resp.get("nextForwardToken")
                        pages.append({"messages": [e["message"] for e in resp["events"]], "ts": [e["timestamp"] - base for e in resp["events"]],
                                      "streams": [e.get("logStreamName") for e in resp["events"]], "nextToken": tok is not None})
                        if not follow or not tok or (api == "get") or len(pages) > 50:
                            break
                        req = dict(req, nextToken=tok)
                    print(json.dumps({"case": cid, "api": api, "query": q, "pages": pages}, ensure_ascii=False))
                except Exception as e:  # record every error verbatim
                    err = getattr(e, "response", {}).get("Error", {"Message": str(e)})
                    status = getattr(e, "response", {}).get("ResponseMetadata", {}).get("HTTPStatusCode")
                    print(json.dumps({"case": cid, "api": api, "query": q, "error": err, "http": status}, ensure_ascii=False))
    finally:
        if logs is not None and not a.keep:
            logs.delete_log_group(logGroupName=group)


if __name__ == "__main__":
    sys.exit(main())
```

Usage: `AWS_PROFILE=sandbox python3 record_g3.py > g3-results.jsonl`, then attach the JSONL to the review that flips N1, N6 and N8. Run `--only C1,C6,C8,C9` first; those four decide the three LOW and MEDIUM rules.

## 10. Notes for the implementer

- Model the compiled pattern as a sum type: `MatchAll`, `Text{required, excluded, optional}`, `JSON{expr}`, `Delimited{fields}`. Parse once at the API boundary; a parse failure is a value that becomes `InvalidParameterException`.
- Put the term test (N1) and the field-count rule (N6) behind one function each. The corpus rows that depend on them are listed in section 8, so a recording changes two functions and a handful of rows.
- Go `regexp` (RE2) after validating the N4 character set is the nearest available engine. Translate `$` to end-of-text. Do not expose flags.
- Property tests that need no oracle: blank pattern matches everything; `-t` is the complement of `t` for a lone term; adding `?` terms to a pattern that has a plain term never changes the result; `A && B` equals `B && A`; `A || B && C` equals `A || (B && C)`; printing a parsed pattern and re-parsing it is the identity; swapping the order of plain terms never changes the result; a JSON pattern never matches a non-JSON message.
- TestMetricFilter is cheap once the matcher exists: same grammar, 50 messages, `eventNumber`, and `extractedValues` keyed `$name` or `$<position>` (see the TestMetricFilter page). It is not in the phase 1 service list, so it stays optional.
- The brief requires terms and JSON patterns for FilterLogEvents. Space-delimited and regex are in the same grammar and are the sources of most DOC vectors; implementing all four costs little more than two.
