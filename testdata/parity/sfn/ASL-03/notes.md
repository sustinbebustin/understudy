# ASL-03 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-03
- account ids masked as <acct> below

## Expected vs AWS

- error name for ResultPath `$.x` on a non-object (4.4: `States.ResultPathMatchFailure` per spec, or `States.Runtime`): differs from both: `States.ReferencePathConflict`
- cause, string input `"foo"`: `Unable to apply ReferencePath $.x to input "foo"` (no `An error occurred while executing the state ...` prefix)
- cause, array input `[1]`: `Unable to apply step "x" to input [1]` (different wording from the string case)
- history: PassStateEntered 2<-0, then ExecutionFailed 3<-2; no PassStateExited
- catchability: n/a (a Pass state cannot carry Catch; the recipe does not test it)
