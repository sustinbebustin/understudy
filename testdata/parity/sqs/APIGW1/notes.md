# APIGW1 (apigateway-rest.md section 13)

- Date: 2026-11-03 (16:35-16:50 UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Script: apigw1.sh (13.1 setup, every case, 13.1 teardown; code blocks verbatim). Account ids below are masked as `<acct>`.

## Session changes (marked `# session:` in the script)

- Helper inputs (echo.py, echo.zip, *.json, t18.vtl, t21.vtl, escape-body.txt) live in `$WORK/APIGW1`, not in the item directory: the script `cd`s there instead of `$REC/APIGW1`. `inv`, `rec` and `recurl` still write captures under `$REC/APIGW1`.
- `$P-role` did not exist and other queues share the account, so the Lambda execution role is `$P-apigw1-fn` (same trust and AWSLambdaBasicExecutionRole as the "only without R03" lines), followed by a 15 s propagation wait, and deleted in the teardown.
- `delete-rest-api` calls (05 and 14 teardowns, the shared teardown, and a rerun for 03 if throttled) go through `delapi`, which retries every 35 s on `TooManyRequestsException`. None was throttled.
- 14's teardown is guarded: it deletes only the id the duplicate create returned.
- 22's `deny` sends nothing when assume-role failed (it did not fail). 22's step 02 cleanup ran only if step 02 created an API (it did not).
- The shared teardown also deletes the log group `/aws/lambda/$P-apigw1-echo`, and waits 30 s before `delete-rest-api`.
- Extra `>/dev/null` on two commands whose output is not a capture.

## APIGW1-01: control-plane error wire form (13.2)

- 01-notfound status 404: matches. `x-amzn-ErrorType: NotFoundException` (bare, no `:http://...` suffix); body `{"message":"Invalid API identifier specified <acct>:us1103-missing"}` (lower-case `message`).
- 01-validation status 400: matches. `x-amzn-ErrorType: ValidationException` (bare); body key lower-case `message`: `1 validation error detected: Value '[WRONG]' at 'createRestApiInput.endpointConfiguration.types' failed to satisfy constraint: Member must satisfy constraint: [Member must satisfy enum value set: [PRIVATE, EDGE, REGIONAL]]`.
- 01-serialization status 400: matches. differs (body key): `x-amzn-ErrorType: SerializationException` (bare), but the body key is capitalised `Message`: `{"Message":"class java.lang.Boolean can not be converted to an String"}`. 2.4 says the restJson1 error body carries `message`.

## APIGW1-02: identifier formats and seeded models (13.3)

- Id lengths and alphabets: matches 3.10 / 12.3 `ids.format`. REST API id `iqia6sj0ff` and root resource id `tz0skq9gak` are 10 characters each. Child resource ids (`l4jqru`, `im89xd`, `l865q0`, `lnlnmb`, `fjkm0k`, `38d5rj`, `ly2m1j`, `x83n80`), the request validator (`uujg42`), models (`pj8mdn`, `hoppwk`, `w21ycu`) and the deployment (`m1dpon`) are 6 characters each. All are lower-case letters and digits.
- Model list: matches 3.9 (`Empty` and `Error` exist on a new API, which settles the open question about `Error`). `Error` has description `This is a default error schema model` and a draft-04 schema titled `Error Schema` with a `message` string. `Empty` has description `This is a default empty schema model`. The list came back ordered by id (`hoppwk` Error, `pj8mdn` M1, `w21ycu` Empty), not by name or creation.
- Side observations: `get-resources` lists `/items` before `/` (ordered by id, see APIGW1-04). The deployment carries only `id` and `createdDate`.

## APIGW1-03: apiStatus after create, update and delete (13.4)

- apiStatus sequence: matches 12.3 `apigateway.restapi.api-status`. Five gets 1 s apart right after create, the update response, and three gets after the update all show `"apiStatus":"AVAILABLE"` with `apiStatusMessage` absent. The status never went through PENDING or UPDATING.
- GetRestApis items carry `apiStatus` (`AVAILABLE`): recorded (3.1 left this open).
- Delete: `DELETE` answered 202 and was not throttled. The first `get-rest-api`, about 1 s later, already answered 404 `NotFoundException` `Invalid API identifier specified <acct>:<id>`, and so did the next two.
- The item logged `exit=1`. That is the status of the final "rerun if throttled" `grep`, which found no throttle, not an early stop.

## APIGW1-04: GetResources order and paging (13.5)

- Order: differs from 12.3 `resources.list-order` ("creation order, root first"). Items come back sorted by resource id in ascending ASCII order, so the root (`tz0skq9gak`) falls in the middle: /id6, /zeta, /mid, /id5, /id2, /id1, /id3, /id4, /id7, /items, /alpha, /, /id8.
- Raw keys: the body has `item` (an array) plus, when `limit` is given, `limit` and `position`. There is no `_embedded`.
- Token format: base64 of `id=S<last id>` (`aWQ9UzlvcHVqcw==` decodes to `id=S9opujs`, and `9opujs` is the id of the last item on page 1), URL-encoded in the JSON (`%3D%3D`).
- Side observation: the recipe's `sed` takes the token already URL-encoded and `--data-urlencode` encodes it again (`%253D`). AWS still returned the right page 2 (/mid, /id5).

## APIGW1-05: deploying an incomplete API (13.6)

- No methods: matches 3.6 / 12.3: 400 `BadRequestException` `The REST API doesn't contain any methods`.
- Method without integration: matches: 400 `BadRequestException` `No integration defined for method`.

## APIGW1-06: stage error texts (13.7)

- Duplicate: matches 12.3: 409 `ConflictException` `Stage already exists`.
- get-stage missing: matches: 404 `NotFoundException` `Invalid stage identifier specified`.
- delete-stage missing: matches: 404 `NotFoundException` `Invalid stage identifier specified`.
- Bad deployment id: matches 3.7: 400 `BadRequestException` `Deployment id does not exist`.
- Name `bad.name`: differs (text, and the allowed set): 400 `BadRequestException` `Stage name only allows a-zA-Z0-9_`. 3.7 quotes the docs, which also allow hyphens.
- 129-character name: differs (text): 400 `BadRequestException` `Stage name must be under 128 characters`. No text was recorded before; 3.7 gives the maximum as 128.

## APIGW1-07: unknown stage, stage root and unknown route (13.8)

- 07-unknown-stage: matches 12.3 `apigateway.invoke.routing`: 403, `x-amzn-ErrorType: ForbiddenException`, `{"message":"Forbidden"}`.
- 07-no-stage (`/`): 403 `ForbiddenException` `{"message":"Forbidden"}`, the same as an unknown stage.
- 07-stage-root and 07-stage-root-slash: 403 `MissingAuthenticationTokenException` `{"message":"Missing Authentication Token"}`. `/s1` and `/s1/` both reach the root resource, which has no method. No `{proxy+}` exists here, so 4.3's root rule is consistent but not fully tested.
- 07-unknown-route: matches finding 3: 403 `MissingAuthenticationTokenException` `{"message":"Missing Authentication Token"}`.
- 07-known-route: 200 `{"marker": "get"}`.

## APIGW1-08: explicit method against ANY, and HEAD (13.9)

- GET with GET and ANY present: matches (explicit method first): marker `get`.
- POST: marker `any`.
- HEAD with GET and ANY present: 200, `Content-Length: 17`, no body. Both markers are 17 bytes, so this capture cannot tell `get` from `any`.
- HEAD on a GET-only resource: matches (HEAD is not served by GET): 403 `MissingAuthenticationTokenException`, `Content-Length: 0`.
- OPTIONS on a GET-only resource: 403 `MissingAuthenticationTokenException` `{"message":"Missing Authentication Token"}`.

## APIGW1-09: 415 text and media-type matching (13.10)

- 415 status: matches.
- 415 body: differs (whitespace): `{"message": "Unsupported Media Type"}`, with a space after the colon. 12.3 `apigateway.mock.selection` gives `{"message":"Unsupported Media Type"}`.
- 415 `x-amzn-ErrorType`: `BadRequestException` (not specified before). Content-Type `application/json`.
- Parameters and case: matches (essence, case-insensitive). `application/json; charset=utf-8` and `Application/JSON` both selected the json template (200 `{"marker": "pt"}`), and so did no Content-Type (defaults to json, 6.1).

## APIGW1-10: integration timeout (13.11)

- Status: matches 12.3 `apigateway.proxy.unrecorded`: 504.
- Body: matches: `{"message": "Endpoint request timed out"}`.
- `x-amzn-ErrorType`: `InternalServerErrorException` (not specified before). The elapsed time was not measured.

## APIGW1-11: response template selection (13.12)

Templates `application/xml` and `text/plain`, none for json.
- Which template renders: differs from 12.3 ("the first Accept media type, else application/json, else the only template"). `Accept: application/xml` gave xml and `Accept: text/plain` gave plain. `Accept: application/json`, no Accept, and `Accept: text/html, text/plain` all gave `<r>xml</r>`. With no exact match on the whole Accept value, AWS falls back to one template (here `application/xml`, the first by name and by order in the map), and a later entry in a list is not matched.
- Response Content-Type: differs: always `application/json`, whichever template rendered (the method response sets no Content-Type).

## APIGW1-12: selectionPattern anchoring and ties (13.13)

- anchor: matches (full match): pattern `40` did not match status 404, and the default (200) answered.
- tie-a (`400=4\d{2}` created before `404=404`): 400 picked.
- tie-b (`404=404` created before `400=4\d{2}`): 400 picked. The order is not creation order.
- tie-c (`499=4\d{2}` created before `401=40\d`): differs from 12.3 ("ascending status-code order"): 499 picked, not 401. Status line `HTTP/1.1 499 ` (no reason phrase). All three ties are consistent with "the longer pattern wins" or "the pattern that sorts last wins". They are neither ascending status, creation order, nor "the literal pattern wins".

## APIGW1-13: empty-string request templates on the wire (13.14)

- PutIntegration answer: matches 12.3 `templates.empty-value`: 201, `"requestTemplates":{"application/json":null,"text/plain":"x"}`.
- GetIntegration answer: matches: 200, the same `null`.

## APIGW1-14: duplicate REST API names (13.15)

- Matches 12.3 `restapi.duplicate-names`: the create succeeded with a new id (`s1w7e6lx6g`, deleted in the teardown).

## APIGW1-15: Lambda proxy without invoke permission (13.16)

- With permission: 200, the event echoed.
- Without permission: matches 12.3 `apigateway.lambda.permission`: 500, `x-amzn-ErrorType: InternalServerErrorException`, `{"message": "Internal server error"}` (60 s after remove-permission).

## APIGW1-16: proxy event edges (13.17)

- GET: `body` null, `isBase64Encoded` false: matches.
- POST with no body, with or without `Content-Type: application/json`: matches 12.3 (`body` null, `isBase64Encoded` false).
- POST text/plain `hello`: `body` `"hello"`, `isBase64Encoded` false.
- POST application/octet-stream, which is in `binaryMediaTypes`: `body` base64 of the zip, `isBase64Encoded` true: matches 5.1.
- Query `a=1&a=2&b=&c&d=x%2By+z`: `queryStringParameters` `{"a":"2","b":"","c":"","d":"x+y z"}`, `multiValueQueryStringParameters` `{"a":["1","2"],"b":[""],"c":[""],"d":["x+y z"]}`. `b=` and bare `c` both give `""`. Last value wins and values are form-decoded: matches 4.5.
- Header casing (REGIONAL): `aUThorization` arrived as `Authorization`, so 4.5's EDGE observation holds without CloudFront too. `X-Multi: 1` and `X-Multi: 2` gave `headers` `"2"` and `multiValueHeaders` `["1","2"]`.
- No `CloudFront-*`, `Via` or `X-Amz-Cf-Id` headers: matches 5.1. `path` `/px`, `requestContext.path` `/s1/px`.

## APIGW1-17: escapeJavaScript (13.18)

Input bytes `22 2f 5c 3c 3e 26 09 c3a9 e282ac 7e` (`"/\<>&<TAB>é€~`).
- e, after the Lambda's JSON decode: `"/\<>&<TAB>é€~`. `"`, `\` and the tab were escaped into valid JSON escapes, since the payload parsed. Whether `/` became `\/` and whether é and € became `\u` escapes cannot be seen: both forms decode the same. The Lambda's own re-serialisation is what reached the `.http` file.
- u: `%22%2F%5C%3C%3E%26%09%C3%A9%E2%82%AC%7E`: UTF-8 percent-encoding, and `~` is encoded as `%7E` (Java URLEncoder).
- b: `Ii9cPD4mCcOp4oKsfg==` (standard base64 of the UTF-8 bytes).

## APIGW1-18: $input.json on missing paths and numbers (13.19)

- missing: `$input.json('$.missing')` renders `""` (an empty JSON string literal, two quote characters).
- n: `10.00` renders `10.0`. So `$input.json` does not keep the original number text, unlike `$input.body`.
- f: `1e3` renders `1000.0`.
- big: `12345678901234567890` renders exactly (integer, no loss).
- all: `{"n":10.0,"f":1000.0,"big":12345678901234567890,"s":"x"}` (compact).
- pathmissing: `$input.path('$.missing')` renders empty.
- Non-JSON body (`not json`): `$input.json('$')` renders `"not json"` (the body as a JSON string). Every other path renders `""`. 200, no error.

## APIGW1-19: AWS integration backend error with only a default response (13.20)

- 19-ok: matches 6.4: 200, JSON `{"SendMessageResponse":{"ResponseMetadata":{"RequestId":...},"SendMessageResult":{"MD5OfMessageAttributes":null,"MD5OfMessageBody":"42b7b4f2921788ea14dac5566e6f06d0","MD5OfMessageSystemAttributes":null,"MessageId":...,"SequenceNumber":null}}}`. Unset members are explicit `null`.
- 19-missing-queue: matches 6.4's likely answer: 200 with the SQS error JSON `{"Error":{"Code":"AWS.SimpleQueueService.NonExistentQueue","Message":"The specified queue does not exist for this wsdl version.","Type":"Sender"},"RequestId":...}`, not the SQS 400.

## APIGW1-20: action URIs without header overrides (13.21)

- PutEvents without an `X-Amz-Target` override: fails, though API Gateway still answers 200. Body: `{"Output":{"__type":"com.amazon.coral.service#UnknownOperationException"},"Version":"1.0"}`. API Gateway does not supply the JSON target for EventBridge (6.4 shows it does for DynamoDB and Step Functions).
- SQS `action/SendMessage` with the form body `QueueUrl=<queue url>&MessageBody=action-uri` and no Content-Type override: refused, though API Gateway still answers 200. Body: `{"Error":{"Code":"AWS.SimpleQueueService.NonExistentQueue","Message":"The specified queue does not exist for this wsdl version.","Type":"Sender"},...}`. The queue existed at the time (19-ok had just sent to it), so the body's `QueueUrl` was not used.

## APIGW1-21: JSONPath beyond simple paths (13.22)

- wild `$.a[*].b`: `[1,2]`.
- deep `$..b`: `[1,2]`.
- filter `$.a[?(@.b > 1)]`: `[{"b":2}]`.
- quoted `$["k e"]`: `"v"`.
- len `$.a.length()`: `2`.
- size `$input.path('$.a').size()`: `2`.
- Every construct is supported. None broke the template: 200.

## APIGW1-22: control-plane IAM denials and resources (13.23)

Every step: 403, `x-amzn-ErrorType: AccessDeniedException`, body key capitalised `Message`. The text is `User: arn:aws:sts::<acct>:assumed-role/us1103-apigw1-caller/gw22 is not authorized to perform: apigateway:<METHOD> on resource: arn:aws:apigateway:us-east-2::<path> because no identity-based policy allows the apigateway:<METHOD> action`.
- 01 get-rest-apis: GET `/restapis` (collection): matches 3.10.
- 02 create-rest-api: POST `/restapis` (collection): matches. Denied, so nothing was created.
- 03 get-rest-api: GET `/restapis/<id>`: matches.
- 04 update-rest-api: PATCH `/restapis/<id>`: matches.
- 05 get-resources: GET `/restapis/<id>/resources`: matches.
- 06 create-resource: POST `/restapis/<id>/resources/<parent-id>`: the parent's item, which settles 3.10's open question. Denied.
- 07 get-method: GET `.../resources/<rid>/methods/GET`: matches.
- 08 get-method-response: GET `.../methods/GET/responses/200`: matches.
- 09 get-integration: GET `.../methods/GET/integration`: matches.
- 10 get-integration-response: GET `.../integration/responses/200`: matches.
- 11 create-deployment (with stageName): POST `/restapis/<id>/deployments`. Only the deployments collection is named. Denied.
- 12 get-stage: GET `/restapis/<id>/stages/s1`: matches.
- 13 get-tags (no policy): differs from 3.10's table (tags form `/tags/<encoded arn>`). The message names GET on the tagged API `/restapis/<id>`, not `/tags/...`. The wire path is `/tags/arn%3Aaws%3Aapigateway%3Aus-east-2%3A%3A%2Frestapis%2F<id>`, with colons and slashes encoded.
- 14 get-tags, `/tags/*` allowed: denied, GET on `/restapis/<id>`.
- 15 tag-resource, `/tags/*` allowed: denied, PATCH on `/restapis/<id>`.
- 16 untag-resource, `/tags/*` allowed: denied, PATCH on `/restapis/<id>`.
- Steps 14-16: the tagged API needs its own permission (GET to read tags, PATCH to change them), as the tagging docs say. Whether `/tags/...` is also required is not shown, because the API check fails first.
