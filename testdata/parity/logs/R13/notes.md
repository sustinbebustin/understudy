# R13

- Date: 2026-10-14T07:02Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Understudy side checked by code reading and a local sealed run (same function code, port 45613).

- setup FunctionUrl (00-create-url-1..10): differs: AWS url-id is 32 chars of [a-z2-7] with a fixed '0' at index 26 (0-based) in all 10 creations, e.g. `pm7otlm6r63bjci5xbwzffvnse0sebuz`, trailing `/`; understudy 32 chars of lowercase base32 with no fixed '0' (internal/service/lambda/state.go:102-104); trailing slash matches (urls_runtime.go:37)
- setup Create response keys (00-create-url-*): matches (FunctionUrl, FunctionArn, AuthType, CreationTime; no InvokeMode, no Cors)
- setup CreationTime precision: differs: AWS 9 fractional digits (`2026-10-14T07:03:14.194926786Z`); understudy 6 (urls_config.go:237-239)
- setup add-permission statements (00-add-permission-url, 00-add-permission-invoke-via-url): matches (string-encoded Statement, Condition StringEquals lambda:FunctionUrlAuthType NONE / Bool lambda:InvokedViaFunctionUrl "true")
- setup Oct 2025 two-statement rule (00-curl-with-url-permission-only, 00-curl-with-both-permissions): matches (only InvokeFunctionUrl: 403, x-amzn-ErrorType AccessDeniedException, `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}`; both: 200) (urls_runtime.go:236-257)
- 1 POST empty body (01-post-empty, 01-logged-events): matches (`body` key absent, isBase64Encoded false, no content-length header)
- 1 text/plain and application/json bodies: matches (text, isBase64Encoded false)
- 1 application/octet-stream and image/png bodies: matches (base64, isBase64Encoded true)
- 1 Content-Encoding gzip + text/plain: matches (gzip bytes base64, isBase64Encoded true, content-encoding header kept; understudy reaches it via the UTF-8 check, furl/event.go:278-287)
- 1 duplicate query (01-get-dup-query): matches (rawQueryString `a=1&a=2&b=3`, queryStringParameters {"a":"1,2","b":"3"}; key absent without a query)
- 1 Cookie header (01-get-cookie): differs: AWS `cookies:["x=1","y=2"]` and also keeps `"cookie":"x=1; y=2"` in headers; understudy drops the cookie header from headers (furl/event.go:210-215)
- 1 x-amzn-trace-id event header: differs: AWS `Root=1-6acf2951-45ed3f7566d55d6558d72e42` (Root only); understudy `Root=...;Parent=...;Sampled=0` (furl/event.go:225-227, invoke.go:811-815)
- 1 requestContext (accountId "anonymous", no authorizer/authentication/pathParameters/stageVariables keys, $default route/stage, time format, timeEpoch number): matches
- 1 x-amzn-tls-cipher-suite / x-amzn-tls-version headers: n/a (understudy serves plain HTTP)
- 1 long path (3003 chars, 01-get-long-path): matches (200, rawPath intact)
- 1 OPTIONS without CORS (01-options-no-cors): matches (function invoked, 200 `echo`)
- 1 HEAD (01-head): differs: AWS 200, Content-Length 0, function invoked; understudy invokes, answers Content-Length 4 with no body (furl/response.go:130-139)
- 1 OPTIONS preflight with CORS (01-options-cors, 10 s after 03-update-cors): differs: AWS invoked the function (200 `echo`, no Access-Control-* headers); understudy answers the preflight itself (200, Access-Control-Allow-Methods/Origin, empty body) (urls_runtime.go:117-122, furl/cors.go:26). Possibly CORS propagation lag on AWS; not settled
- 2 bare object (02-resp-obj): matches (200 application/json `{"a":1}`, re-serialized compactly)
- 2 string (02-resp-str): matches (body `hello`, no JSON quotes, Content-Length 5)
- 2 number, null (02-resp-num, 02-resp-null): matches (`123`, `null`)
- 2 statusCode 99 (02-resp-sc99): differs: status/body match (502 `Internal Server Error`) but AWS Content-Type application/json; understudy text/plain (furl/response.go:145)
- 2 statusCode 200 with body 123 (02-resp-body123): differs: AWS 200 body `123`; understudy 502 (furl/response.go:85-88)
- 2 cookies (02-resp-cookies): matches (one Set-Cookie each)
- 2 multiValueHeaders (02-resp-mvh): matches (ignored; headers passed; understudy canonicalizes the name to `X-H`, AWS sends `x-h`)
- 2 isBase64Encoded with invalid base64 (02-resp-badb64): differs: AWS 200 with leniently decoded bytes (`not base64!!` -> 6 bytes 9e8b5b6ac7ba); understudy 502 (furl/response.go:97-101)
- 2 raise (02-resp-raise): differs: 502 `Internal Server Error` matches; Content-Type application/json vs understudy text/plain (furl/response.go:145)
- 2 timeout (02-resp-timeout): differs: same as raise (Content-Type only)
- 2 oversize body 6 MiB+10 (02-resp-big): differs: AWS 200 with Content-Length 6291466; understudy 200 with the same body but Transfer-Encoding chunked (furl/response.go:130-139)
- 2 errorMessage object (02-resp-errmsg): differs: AWS 200 body `{"errorType":"Custom","errorMessage":"custom"}` (keys reordered); understudy `{"errorMessage":"custom","errorType":"Custom"}` (furl/response.go:42-45)
- 2 whoami (02-resp-whoami): n/a (AWS answered 502; the function failed, cause not captured)
- 3 get-function-url-config InvokeMode (03-get-url-config): matches (`InvokeMode: BUFFERED` and LastModifiedTime echoed on Get) (urls_config.go:99-113)
- 3 update-function-url-config response (03-update-cors): matches (Cors echoed, no InvokeMode) (urls_config.go:174)
- 4 invoke empty payload (04-payload-empty): matches (function receives `{}`) (invoke.go:508-510)
- 4 invoke null payload (04-payload-null): matches (200, body `null`)
- 4 invoke not-JSON (04-payload-not-json): differs: same status, code, Type and prefix; AWS tail is Jackson's `Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at [Source: REDACTED (...); line: 1, column: 1]`, understudy Go's `invalid character 'o' in literal null (expecting 'u')` (invoke.go:857-861)
- 4 invoke Event (04-event): matches (202, Content-Length 0, no Content-Type)
- 4 invoke success Content-Type (04-raise, 04-errmsg, 04-payload-*): differs: AWS application/json; understudy application/octet-stream (internal/protocol/restbind/response.go:88)
- 4 X-Amz-Function-Error for a raise (04-raise): matches (`Unhandled`, body errorMessage/errorType/requestId/stackTrace)
- 4 X-Amz-Function-Error for a returned {"errorMessage"} (04-errmsg): matches (absent; payload passed through verbatim)
- 4 Event payload over 1 MB (04-event-over-1mb): differs: AWS 413 RequestEntityTooLargeException `{"Message":"1048688 byte payload is too large for the Event invocation type (limit 1048576 bytes)"}` (capital Message, no Type); understudy 413 `{"message":"Request must be smaller than 1048576 bytes for the InvokeFunction operation"}` (invoke.go:853-855, codegen/overlays/common.json:354-357)
- 4 DryRun on a Pending function (04-dryrun-pending): differs: AWS 409 ResourceConflictException `{"Type":null,"message":"The operation cannot be performed at this time. The function is currently in the following state: Pending"}`; understudy same text with `"Type":"User"` (errors.go:75-77), reachable only under lambda.stateTransitions clock
- 4 create-function State (04-create-oom): n/a (AWS Pending / "The function is being created." / Creating; understudy Active by the lambda.stateTransitions final default)
- 4 OOM invoke (04-invoke-oom): matches (200, Unhandled, `{"errorType":"Runtime.OutOfMemory","errorMessage":"RequestId: <id> Error: Runtime exited with error: signal: killed"}`)
- 4 weighted alias X-Amz-Executed-Version (04-create-weighted-alias, 04-invoke-alias-1..4): n/a (CLI rejected the --routing-config syntax; alias never created)
- 4 invoke of a missing alias (04-invoke-alias-1..4): matches (404 ResourceNotFoundException `Function not found: arn:aws:lambda:us-east-2:123456789012:function:usr-rec-20261014-url:live`, Type User)
- 5 reserved env key AWS_REGION (05-env-aws-region): matches (400 InvalidParameterValueException, Type User, `Lambda was unable to configure your environment variables because the environment variables you have provided contains reserved keys that are currently not supported for modification. Reserved keys used in this request: AWS_REGION`)
- 5 timeout 901 (05-timeout-901): differs: AWS 400 ValidationException `{"message":"Value '901' at 'timeout' failed to satisfy constraint: Member must have value less than or equal to 900"}` (no `1 validation error detected: ` prefix); understudy adds the prefix (internal/awserr/violation.go:162-177)
- 5 memory 10241 (05-memory-10241): differs: AWS 400 ValidationException `{"message":"'MemorySize' value failed to satisfy constraint: Member must have value less than or equal to 3008"}` (free-plan account quota); understudy `1 validation error detected: Value '10241' at 'memorySize' failed to satisfy constraint: Member must have value less than or equal to 10240`
- 5 memory 127 (05-memory-127): n/a (rejected client-side by the CLI)
- 6 version numbers after deleting version 2 (06-delete-version-2, 06-publish-after-delete, 06-publish-again-unchanged): matches (delete 204; next publish is 3, not 2; unchanged publish returns 3) (state.go:115-117)
- 7 identity inside the function: n/a (02-resp-whoami returned 502 on AWS)
- 8 log stream name (08-describe-log-streams, 08-describe-log-streams-oom): matches (`2026/10/14/[$LATEST]<32 hex>`)


### Re-recorded 2026-10-14: weighted alias (step 4)
- 04-create-weighted-alias and 04-invoke-alias-1..6: X-Amz-Executed-Version is the version actually run (4,4,4,5,5,5 over six calls at weight 0.5). The function had been deleted and recreated under the same name, and its versions continued at 4 and 5: Lambda does not reuse version numbers of a deleted function with the same name. understudy: weighted routing and the executed-version header follow internal/service/lambda (not compared byte for byte); version numbering restarts at 1 for a recreated function (differs).
