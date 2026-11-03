# ASL-11 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-11
- account ids masked as <acct> below

## Expected vs AWS

- which catcher ran (sfn.timeout.heartbeat-error-name, default: `States.Timeout` reported, `States.HeartbeatTimeout` also matches): matches -- the `HB` catcher (ErrorEquals `States.HeartbeatTimeout`, listed first) ran; output `"heartbeat"`. Since HB is listed before TO, this does not show which wins when `States.Timeout` comes first.
- ActivityTimedOut details: matches 8.6 -- `{"error":"States.Timeout"}`, no cause key; 5 s after ActivityStarted (16:51:03.256 -> 16:51:08.256)
- error output handed to the catcher: `{"Error":"States.Timeout","Cause":""}` -- `Cause` present as the empty string when the error has no cause (also answers ASL-13 step 05's question for this path)
- history: TaskStateEntered 2<-0, ActivityScheduled 3<-2 (`timeoutInSeconds` 120, `heartbeatInSeconds` 5), ActivityStarted 4<-3 (`workerName` `rec`), ActivityTimedOut 5<-4, TaskStateExited 6<-5 (output the error output), PassStateEntered HB 7<-6, PassStateExited 8<-7, ExecutionSucceeded 9<-8
