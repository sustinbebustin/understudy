# ASL-06 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-06
- account ids masked as <acct> below

## Expected vs AWS

The execution FAILED at the Map: Pass `A` has no ResultPath, so its Parameters output replaces the state data and the Map's `ItemsPath` `$.r` no longer exists -- `States.ReferencePathConflict`, cause `Unable to apply step "r" to input {<Pass A output>}` (MapStateEntered 4<-3, ExecutionFailed 5<-4). Pass A's output (MapStateEntered input) answers every field but `randoms`.

- unique (is 1 equal to 1.0?): `[1.0,[1],{"a":1},null,1,"1",true]` -- `1` and `1.0` are distinct; duplicate arrays, objects, nulls and booleans removed; output order is not input order (hash-set order)
- contains1 (`States.ArrayContains([1.0,"1"], 1)`): `true` -- here 1.0 equals 1 (unlike ArrayUnique)
- fmt rendering (sfn.intrinsic.format-container, Java): `1.0 1.0E21 -0.0 0.1 100000000000000000000` -- `1.0` keeps `.0`, `1e21` renders `1.0E21`, `-0.0` kept, big integer kept exact
- json (`States.JsonToString`): `[1.0,1.0E21,-0.0,0.1,100000000000000000000]` -- same number renderings, not JSON-normalised
- seeded1 == seeded2: yes, both `236` for `States.MathRandom(0, 1000, 7)`
- frac (`States.MathRandom(0.6, 2.4)`): `1` (consistent with bounds rounded to 1 and 2, end exclusive; one sample)
- randoms ever contains 2 (end exclusivity): n/a (recipe defect: the Map never ran, see above; fix: carry `r` through Pass A, e.g. add `"r.$":"$.r"` to its Parameters, or give A a ResultPath)
