# SFN-02 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.3
- account ids masked as <acct> below

## Expected vs AWS

- token shape (sfn.task-token.format): recorded in token-shape.txt (length and first characters); not re-read here (the capture holds a live token; redact before sharing)
- 01 InvalidOutput text: `InvalidOutput` `Invalid Output: 'not json' is not valid JSON.` (doc 7.1 had only the API-reference text; differs from that)
- 02 heartbeat on a live token: matches -- 200 `{}`
- 03 altered token (InvalidToken or TaskDoesNotExist?): differs: neither -- `TaskTimedOut` `Task Timed Out: 'Provided task does not exist.'`
- 04-06 success/heartbeat/failure after TimeoutSeconds (sfn.task-token.closed-error): differs: code matches (`TaskTimedOut`) but text is `Task Timed Out: 'Provided task does not exist anymore'` (no period), not `Task Timed Out: '<token>'`
- 07 success: matches -- 200 `{}`
- 08 success again on a completed task: differs: `TaskTimedOut` `Task Timed Out: 'Provided task does not exist anymore'` (same as after a timeout)
- 09 history: recorded (TaskTimedOut path)
