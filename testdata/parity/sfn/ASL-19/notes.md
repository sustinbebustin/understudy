# ASL-19 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-19
- account ids masked as <acct> below

## Expected vs AWS

- creation accepts each construct (UNVERIFIED 4.5: `..`, union, negative index, `length()`, `&&` filter, `[*]`): all accepted -- CreateStateMachine 200, no create error, so no key was removed and nothing retried
- `len.$` `$.a.length()`: differs from Jayway JsonPath (which has `length()`): fails at run time -- `States.Runtime`, cause `An error occurred while executing the state 'A' (entered at the event id #2). The JSONPath '$.a.length()' specified for the field 'len.$' could not be found in the input '{"a":[1,2,3],"o":[{"id":1,"x":1,"y":2},{"id":2,"x":2,"y":2}]}'`; history PassStateEntered 2<-0, ExecutionFailed 3<-2
- values of `desc` (`$..id`), `union` (`$.a[0,2]`), `neg` (`$.a[-1]`), `and` (`&&` filter), `star` (`$.o[*].x`): n/a (recipe gap: the `length()` failure ends the execution before any output is written; the recipe's retry covers only create errors; fix: drop `len.$` and run again)
- session: 16.3 "Teardown for the session" deletes every `$P` machine and activity by prefix; other recordings share P=us1103, so the script deletes only the exact names this queue created (asl.sh), and only those still present; all were gone afterwards
