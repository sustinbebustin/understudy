# ASL-04 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-04
- account ids masked as <acct> below

## Expected vs AWS

- Parallel `Parameters` missing path (sfn.path.missing-in-parameters, default States.Runtime): matches -- `States.Runtime`, and the Parallel's `States.ALL` catcher did not catch it (terminal); cause `An error occurred while executing the state 'P' (entered at the event id #2). The JSONPath '$.nope' specified for the field 'v.$' could not be found in the input '{"k": "v", "n": [1, 2]}'`; history ParallelStateEntered 2<-0, ExecutionFailed 3<-2 (no ParallelStateStarted)
- Map `ItemSelector` missing path: matches -- `States.Runtime`, cause `An error occurred while executing the state 'M' (entered at the event id #2). The JSONPath '$.nope' specified for the field 'v.$' could not be found in the input '{"items": [1], "k": "v"}'`; history MapStateEntered 2<-0, MapStateStarted {length:1} 3<-2, ExecutionFailed 4<-3 -- the failure comes after MapStateStarted and points at it, not at the Entered event (11.2 rule 8 says the state's `*StateEntered`), and no MapIterationStarted is written
- input serialisation in the cause (UNVERIFIED 4.6, compact or as sent): as sent -- the spaces of the execution input survive (`{"k": "v", "n": [1, 2]}`); for Map the quoted input is the Map's effective input (the whole state input), not the item
