date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1

Session: helper files under $REC/_work/LDL (# session:). Account concurrency limit 10, unreserved 10.

- 01 reserve 1: refused, as the recipe's comment foresaw: 400 `InvalidParameterValueException` (header `x-amzn-ErrorType`), body `{"Type":null,"message":"Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [10]."}`. The flood ran against the account limit of 10 without a reservation.
- `EventAgeExceeded` records, count 0, no response half: n/a (not provoked): none of the 60 events aged out; the drains 02-destination-1..6 and 03-dlq-1..6 hold no `us1103-ldl-th` record or DLQ message at all (they hold only late LDL-01/02/03 messages: see those notes). With 60 events x 2 s at concurrency 10 the async queue absorbed the throttles well inside the 60 s age limit. A free-plan account cannot reserve 1, so this case needs another way to throttle (for example a longer sleep, or more events) to settle 3.3 item 5.
- DLQ `ErrorCode` for an aged-out throttled event (guess 429): n/a (not provoked, as above).
- 04 reserve 0: accepted, 200 `{"ReservedConcurrentExecutions":0}`.
- zero-concurrency record: matches finding 5 / 3.4: `{"version":"1.0",...,"requestContext":{...,"condition":"ZeroReservedConcurrency","approximateInvokeCount":0},"requestPayload":{"mode":"ok"}}`, no `responseContext` or `responsePayload`.
- DLQ for `ZeroReservedConcurrency`: differs from the expectation that the three attributes appear (5.2): the message has only `ErrorMessage` (String) `Function not invoked. Function isconfigured with zero reserved concurrent executions` (sic, no space in `isconfigured`) and `RequestID` (String); there is no `ErrorCode` attribute. Body is the payload `{"mode":"ok"}`.
