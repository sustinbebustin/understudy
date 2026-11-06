# ASL-09 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-09
- account ids masked as <acct> below

## Expected vs AWS

- 01 TimestampPath with offset `+01:00` (UNVERIFIED 7.1): accepted -- past time, WaitStateEntered 2<-0 and WaitStateExited 3<-2 at the same millisecond, SUCCEEDED
- 02 TimestampPath with lower-case `t` ... `z` (`2016-03-14T01:59:00z`): accepted -- SUCCEEDED, no error (7.1's documented upper-case `T`/`Z` rule is not enforced at run time for a lower-case `z`)
- 03 SecondsPath holding the string `"1"`: accepted -- the wait lasted about 1.04 s (Entered 16:48:03.070, Exited 16:48:04.106), SUCCEEDED (a numeric string is parsed as seconds)
- 04 SecondsPath 31622400 (366 days, over the one-year maximum): accepted -- after 10 s the history is only ExecutionStarted and WaitStateEntered 2<-0; no failure, no `Wait state delay is too long.`; the script then stopped it
- 05 literal Seconds 100000000: 400 `InvalidDefinition` `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: Maximum value is 99999999 at /States/W/Seconds'` (matches 3.7's 0..99,999,999)

## Replay (internal/parity)

`scenario.json` replays step 05; internal/parity/baseline.json holds the outcome.

- Not replayed: 00 to 04, whose success bodies differ by member order alone and whose execution timings the frozen replay clock does not reproduce.
