# R10

- Date: 2026-10-14T06:48Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy at the working tree of 2026-10-14 (replayed locally via scripts/sealed).

- 02 put-log-events 14 events: matches (200 {nextSequenceToken})
- 03 filter-01 `ERROR`: matches (substring: ERRORS, ERROR:, [ERROR], MY_ERROR, ERRORx, status=ERROR, JSON line match; `error` does not)
- 03 filter-02 `"ERROR"`: matches (same 12 as 01)
- 03 filter-03 `?ERROR ?INFO`: matches (13, adds the INFO JSON line)
- 03 filter-04 `ERROR -an`: matches (11, drops "an ERROR occurred")
- 03 filter-05 `ERROR occurred`: matches (1)
- 03 filter-06 `"an ERROR"`: matches (1)
- 03 filter-07 `{ $.level = "ERROR" }`: matches (1)
- 03 filter-08 `{ $.n > 3 }`: matches (1)
- 03 filter-09 `{ $.level = "ERROR" && $.n >= 5 }`: matches (1)
- 03 filter-10 `{ $.missing NOT EXISTS }`: matches (both JSON lines; non-JSON lines excluded)
- 03 filter-11 `[a, b, c]`: matches ("an ERROR occurred" and the 4-word "a b ERROR c"; fewer than 3 fields never match)
- 03 filter-12/13 `ERR*`, `*ROR`: differed: AWS 400 InvalidParameterException "Invalid character(s) in term '*'"; understudy "Invalid character(s) in term ERR*" / "... *ROR". Matches since commit 0aa0b14, which quotes only the offending characters (internal/logsfilter/error.go).
- 03 filter-14 `{ $.n = }`: differs: AWS 400 InvalidParameterException "Invalid filter pattern"; understudy "Invalid filter pattern: comparison value expected at offset 8." (internal/logsfilter/error.go:77-79, scanner.go:143)
- 03 response shape: differs: AWS sends nextToken even when every match is on the page, and an unmodeled searchStatistics; understudy omits nextToken when nothing remains (internal/service/logs/filter.go:82-84); searchedLogStreams [] matches
- 04 filter-log-events start T, end T+1: matches (b0, b1: both bounds inclusive)
- 04 filter-log-events start T+1: matches (b1, b2)
- 04 get-log-events start T, end T+1: matches (b0 only: endTime exclusive)
- 04 get-log-events start T+1: matches (b1, b2: startTime inclusive)
- 05 filter-log-events --limit 3 paging: differs: AWS pages 3, 3, 1 each with nextToken, then a 4th empty page {"events":[]} without nextToken; understudy pages 3, 3, 1 with no token on the 3rd (filter.go:82-84)
- 05 get-log-events --limit 3 paging: differs: AWS pages 3, 3, 1, then an empty page whose nextForwardToken differs from page 3's, then an empty page with the same token (stable on call 5); understudy's first empty page already repeats page 3's nextForwardToken (stable on call 4) (internal/service/logs/events.go:336-344). AWS also sends an unmodeled eventId per event
- 06 span over 24 h: matches (400 "The batch of log events in a single PutLogEvents request cannot span more than 24 hours.")
- 06 older than 14 days: matches (200 rejectedLogEventsInfo {"tooOldLogEventEndIndex":1}, no retention set)
- 06 newer than 2 h: matches (200 rejectedLogEventsInfo {"tooNewLogEventStartIndex":0})
- 06 out of order: matches (400 "Log events in a single PutLogEvents request must be in chronological order.")
- 06 empty message: n/a (CLI rejects client-side, never sent)
- 06 message of 262145 bytes: matches (200, accepted)
- 06 10001 events: differs: AWS 400 "1 validation error detected: Value at 'logEvents' failed to satisfy constraint: Member must have length less than or equal to 10000" (no value echoed); understudy "Value '[, , , ...]' at 'logEvents' ..." (internal/codegen/emit_validate.go:552-554 renders a list of structures)
- 06 put-retention-policy 6: differs: AWS 400 InvalidParameterException "Invalid retention value. Valid values are: [1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653]"; understudy "1 validation error detected: Value '6' at 'retentionInDays' failed to satisfy constraint: Member must satisfy enum value set: [...]" (internal/service/logs/validate.go:21-38)
- 06 put-retention-policy 7: matches (200, empty body)
- 07 Lambda log stream name: n/a (not run in this item)

