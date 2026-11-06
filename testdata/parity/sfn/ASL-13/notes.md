# ASL-13 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-13
- account ids masked as <acct> below

## Expected vs AWS

- 01 Parallel event order (sfn.history.concurrent-order, default branch or item index first, then step): differs for Parallel: events interleave across branches -- A1 Entered 4<-3, A1 Exited 5<-4, B1(Wait) Entered 6<-3, C1 Entered 7<-3, C1 Exited 8<-7, A2 Entered 9<-5, A2 Exited 10<-9, B1 Exited 11<-6 (2 s later); each branch's first event points at ParallelStateStarted 3
- 01 ParallelStateSucceeded previousEventId: 12<-11 (the last inner event, B1's WaitStateExited, i.e. the last branch to finish, not the last branch in order); ParallelStateExited 13<-11 also points at it; ParallelStateSucceeded has empty details; output `[{},{},{}]`
- 02 Parallel with a failing branch: error name is the branch's own `BranchErr` (not `States.BranchFailed`), cause `c`; history ParallelStateStarted 3<-2, FailStateEntered 4<-3, ParallelStateFailed 5<-4 (empty details), ExecutionFailed 6<-5; the other branch's Wait never started -- no WaitStateEntered and no WaitStateAborted
- 03 Map MaxConcurrency 0 order: matches "index first": all MapIterationStarted (4..8, each <-3) first, then each iteration's events complete in index order (S Entered <- its MapIterationStarted, S Exited, MapIterationSucceeded <- S Exited); MapStateSucceeded 24<-23 and MapStateExited 25<-23 share the last iteration's MapIterationSucceeded as parent (11.2 rule 5); unlike 11.3's MaxConcurrency 1 record, every MapIterationStarted points at MapStateStarted
- 04 Map with a failing item: iteration 1 FailStateEntered 14<-10; then MapIterationAborted index 0 15<-14, WaitStateAborted 16<-14 (empty details), MapIterationFailed index 1 17<-14, MapStateFailed 18<-14 (empty details), ExecutionFailed 19<-18 `{"error":"ItemErr"}` with no cause key; iteration 2 had passed its Choice (12) but never entered W and got no MapIterationAborted event
- 05 error output Cause when the error has none (UNVERIFIED 8.4: absent, "" or null): `null` -- the catcher's output is `{"Error":"NoCause","Cause":null}` (Error first). Contrast ASL-11, where a States.Timeout without cause gave `"Cause":""`. History: FailStateEntered 4<-3, ParallelStateFailed 5<-4, ParallelStateExited 6<-5, SucceedStateEntered 7<-6
