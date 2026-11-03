# ASL-07 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-07
- account ids masked as <acct> below

## Expected vs AWS

The execution FAILED in the Map at item 7 (`null`): `States.Runtime`, cause `An error occurred while executing the state 'C' (entered at the event id #26). Unable to apply Path transformation to null or empty input.` (Choice `Variable` `$` on a null input). It failed before any Pass state of any iteration was entered, so no T/F result was written and the `Cmp` Parallel never ran.

- IsTimestamp T/F array: n/a (recipe defect: the `null` item fails the execution first; the other seven iterations wrote ChoiceStateExited but no next-state event before ExecutionFailed; fix: drop `null` from `v` or test it in a separate run)
- `null` with Choice Variable `$`: `States.Runtime` `Unable to apply Path transformation to null or empty input.` (new recorded text)
- TimestampEqualsPath across precision (a vs b) and offset (a vs c): n/a (not reached, same defect)
- StringMatches escapes `a\*b\\c*`: n/a (not reached, same defect)
- 02 open escape `abc\` (UNVERIFIED 6.3, creation or run time?): accepted at validation -- `result OK`, no diagnostic (so any failure would be at run time)
- Map concurrency observed: all eight MapIterationStarted events (4..11) point at MapStateStarted 3; each iteration's ChoiceStateEntered points at its own MapIterationStarted; ExecutionFailed 27<-26 (the null iteration's ChoiceStateEntered)
