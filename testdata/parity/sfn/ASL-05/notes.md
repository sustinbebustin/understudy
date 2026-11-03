# ASL-05 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-05
- account ids masked as <acct> below

## Expected vs AWS

Input `{"a":[1],"s":"%%%","n":2147483647,"o":{"k":{"x":1}}}`; each expression in a Pass inside a Parallel whose `States.ALL` catcher would catch a catchable error.

- create-time checks: none -- all eleven definitions were created (no `InvalidDefinition`)
- error name (sfn.intrinsic.failure-error, default `States.IntrinsicFailure`, catchable): differs: every failure is `States.Runtime`, not caught by the Parallel's `States.ALL` catcher; history ... PassStateEntered 4<-3, ExecutionFailed 5<-4 (no PassStateExited, no ParallelStateFailed)
- cause form: `An error occurred while executing the state 'A' (entered at the event id #4). There was an error while evaluating the intrinsic function: <expression as written>. <detail>`
- 01 `States.ArrayGetItem($.a, 5)` (index out of range): differs: no error -- succeeds with `{"r":null}`
- 02 `States.ArrayGetItem($.a, -1)`: `States.Runtime`, detail `Invalid arguments in States.ArrayGetItem`
- 03 `States.ArrayPartition($.a, 0)`: `States.Runtime`, `Invalid arguments in States.ArrayPartition`
- 04 `States.ArrayRange(1, 1001, 1)` (1001 items): `States.Runtime`, `Invalid arguments in States.ArrayRange`
- 05 `States.ArrayRange(1, 5, 0)`: `States.Runtime`, `Invalid arguments in States.ArrayRange`
- 06 `States.Base64Decode($.s)` with `%%%`: differs: no error -- succeeds with `{"r":""}`
- 07 `States.Hash('x', 'SHA-3')`: `States.Runtime`, `Invalid arguments in States.Hash`
- 08 `States.StringToJson($.s)`: `States.Runtime`, `Cannot convert String to Json in States.StringToJson, caused by: Unexpected character ('%' (code 37)): expected a valid value (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at [Source: REDACTED (`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION` disabled); line: 1, column: 1]` (the `REDACTED` is Jackson's own text in AWS's cause, not session redaction)
- 09 `States.JsonMerge($.o, $.o, true)`: `States.Runtime`, `Deep merge not supported in States.JsonMerge`
- 10 `States.MathAdd($.n, 1)` (2147483647 + 1): `States.Runtime`, `Invalid arguments in States.MathAdd`
- 11 `States.Format('{} {}', 1)`: `States.Runtime`, `Invalid arguments in States.Format: number of arguments do not match the occurrences of {}.`
- Parallel success linkage (01, 06): ParallelStateSucceeded 6<-5 and ParallelStateExited 7<-5, both pointing at the branch's last event (confirms 11.2 rule 5 for Parallel; ParallelStateSucceeded has empty details)
