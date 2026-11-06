# ASL-08 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-08
- account ids masked as <acct> below

## Expected vs AWS

- 01 no match, no Default (sfn.choice.no-match, default `States.NoChoiceMatched`, catchable): differs: `States.Runtime`, not caught by the Parallel's `States.ALL` catcher; cause `An error occurred while executing the state 'C' (entered at the event id #4). Failed to transition out of the state. The state does not point to a next state.`; history ParallelStateEntered 2<-0, ParallelStateStarted 3<-2, ChoiceStateEntered 4<-3, ExecutionFailed 5<-4
- 02 missing Variable (sfn.choice.missing-variable, default `States.Runtime`): matches -- `States.Runtime`, uncaught; cause `An error occurred while executing the state 'C' (entered at the event id #4). Invalid path '$.x': The choice state's condition path references an invalid value.` (the text 6.4 found only in a blog is AWS's)
- 03 string "1" against NumericEquals 1: no match (type mismatch is false, 6.2) -> same as 01: `States.Runtime` `Failed to transition out of the state. The state does not point to a next state.`, uncaught
