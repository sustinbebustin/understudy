# SFN-15 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.16
- account ids masked as <acct> below

## Expected vs AWS

- session: helper files (sfn15.py, sfn15.zip) written under $WORK/SFN-15/ instead of the shell's cwd
- 01 status (sfn.denial.status, default AccessDeniedException at 400): matches -- `HTTP/1.1 400 Bad Request`
- 01 `__type`: `com.amazon.coral.service#AccessDeniedException` (Coral namespace, unlike the `com.amazonaws.swf.service.v2.model#` service errors)
- 01 message key: differs from every other Step Functions error: capital `Message`, not `message`
- 01 text: matches the IAM grammar (iam-enforcement-surfaces.md 4.1) -- `User: arn:aws:iam::<acct>:user/us1103-sfn-nobody is not authorized to perform: states:ListStateMachines on resource: arn:aws:states:us-east-2:<acct>:stateMachine:* because no identity-based policy allows the states:ListStateMachines action`
- 02 lambda:invoke without lambda:InvokeFunction (sfn.integration.lambda-error): matches -- `TaskFailed` error `Lambda.AWSLambdaException`, cause `User: arn:aws:sts::<acct>:assumed-role/us1103-sfn-noinv/<32-letter session name> is not authorized to perform: lambda:InvokeFunction on resource: arn:aws:lambda:us-east-2:<acct>:function:us1103-sfn15-fn because no identity-based policy allows the lambda:InvokeFunction action (Service: AWSLambda; Status Code: 403; Error Code: AccessDeniedException; Request ID: <uuid>; Proxy: null)`; then `ExecutionFailed` 6<-5
- 03 legacy function ARN: error `Lambda.AWSLambdaException` in `LambdaFunctionFailed` matches; fails after `LambdaFunctionScheduled` (3<-2) and `LambdaFunctionStarted` (4<-3, no details); differs in the cause form: SDK v2 style `... action (Service: Lambda, Status Code: 403, Request ID: <uuid>) (SDK Attempt Count: 1)` -- not the switch's `(Service: AWSLambda; Status Code: 403; Error Code: AccessDeniedException; ...; Proxy: null)`, which only the optimized integration (02) uses
- 04 reserved concurrency 0: matches -- `TaskFailed` `Lambda.TooManyRequestsException`, cause `Rate Exceeded. (Service: AWSLambda; Status Code: 429; Error Code: TooManyRequestsException; Request ID: <uuid>; Proxy: null)`
- the execution role session name is a random 32-letter string (`lywTUPWXVUlmFcljKYeVeRrILudCUcAw`)
