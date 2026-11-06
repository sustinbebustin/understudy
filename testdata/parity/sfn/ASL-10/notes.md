# ASL-10 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-10
- account ids masked as <acct> below

## Expected vs AWS

Delay = time from a FailStateEntered to the next ParallelStateStarted; every measured delay is a whole number of seconds plus 30-50 ms of processing.

- 01 Interval 1, Backoff 1.5, MaxAttempts 4 (sfn.retry.fractional-interval, default keep milliseconds; expected 1, 1.5, 2.25, 3.375 s "or rounded"): differs from the default: 1.03, 1.03, 2.03, 3.05 s -- each delay truncated (floored) to whole seconds: 1, 1, 2, 3
- 02 Interval 2, Backoff 3, MaxAttempts 4, MaxDelaySeconds 5: matches -- 2.03, 5.03, 5.05, 5.04 s (2, 5, 5, 5)
- 03 FULL jitter, Interval 4, Backoff 2, MaxAttempts 6 (sfn.retry.full-jitter, uniform in [0, d_k]): within bounds -- 4.03, 6.04, 11.04, 6.04, 24.05, 74.04 s against caps 4, 8, 16, 32, 64, 128; whole seconds only (integer granularity); one sample, the distribution is not settled
- events a Parallel retry writes: no ParallelStateFailed between attempts; each retry is a new `ParallelStateStarted` pointing at the previous attempt's last event (FailStateEntered), e.g. 3<-2, 4<-3, 5<-4, 6<-5 ...; after the last attempt ParallelStateFailed 13<-12 (empty details) and ExecutionFailed 14<-13 `{"error":"E1","cause":"c"}`; no ParallelStateEntered is repeated; the branch's Fail error E1 is the Parallel's error (answers 7.2: the branch's own error, not `States.BranchFailed`)
