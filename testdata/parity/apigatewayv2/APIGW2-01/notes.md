# APIGW2-01: control-plane error texts and validation (apigateway-http.md 14.2)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)
- Script: scratchpad later/APIGW2/apigw2.sh (14.1 verbatim; only session change in this item: none)

Setup finding (14.1): `create-route --route-key 'GET /slash/'` answered `BadRequestException` "Part of the given route key path is empty". A trailing slash leaves an empty last path part, so AWS refuses it. The route does not exist; APIGW2-05 steps 10 and 11 therefore record what `/slash/` and `/slash` get without it.

## Expected (14.2 "Settles")

- 2.3 texts and header form: matches the shapes (`NotFoundException` 404, `ConflictException` 409, `BadRequestException` 400, `message` key). Recorded texts: GetApi/DeleteApi `Invalid API identifier specified <account>:0000000000` (the REST text, now confirmed for v2); route `Invalid route identifier specified`; integration `Invalid Integration identifier specified`; stage `Invalid stage identifier specified`; deployment `Invalid Deployment identifier specified`; authorizer `Invalid Authorizer identifier specified`.
- Body formatting: differs: some bodies are pretty-printed (`{\n  "message" : "..."\n}`, GetApi, GetStage, CreateStage) and others are compact (`{"message":"..."}`, DeleteApi, routes, integrations), so the format depends on the operation.
- `x-amzn-ErrorType` suffix: matches no-suffix reading: the header is the bare name (`NotFoundException`, `BadRequestException`, ...) with no `:http://internal.amazon.com/coral/...` suffix.
- `resourceType`: differs: none of the seven 404 bodies carries `resourceType`, only `message`.
- Duplicate stage (08): `ConflictException` 409 "Stage already exists".
- Duplicate route key (09): `ConflictException` 409 "Route with key GET /any already exists for this API".
- Bad method (10) and path without slash (11): `BadRequestException` "The provided route key is not formatted properly for HTTP protocol. Format should be \"[HTTP METHOD] /[RESOURCE PATH]\" or \"$default\"".
- Greedy not last (12): `BadRequestException` "Greedy variables may only be in last position of route key".
- Literal text beside a variable (13, 3.2): refused: `BadRequestException` "One or more path parts appear to be invalid. Parts are validated against this regular expression: ^(\{[\w.:-]+\+?\}|[a-zA-Z0-9.:_-]+)$". Each path part is a whole variable or literal of `[a-zA-Z0-9.:_-]`.
- Target naming a missing integration (14): `BadRequestException` "Unable to find route target of type integrations with ID 0000000".
- Bad stage name (15): `BadRequestException` "Stage name only allows a-zA-Z0-9_". The text omits the hyphen that 2.4 allows.
- Bad stage variable value (16, 2.4/13.5 `[A-Za-z0-9-._~:/?#&=,]+`): differs: `k=a b` was accepted (201, stage `badvar` created with `stageVariables {"k":"a b"}`). AWS does not enforce the documented value pattern on CreateStage, at least not for a space.
- Model range failure (17, 2.3/12 item 11 identity): `BadRequestException` 400 "Timeout should be between 50 ms and 30000 ms". It is not REST's `ValidationException`.
- Model enum failure (17b): differs from both readings: `BadRequestException` 400 "IntegrationType is a required parameter for creating integration." AWS drops the unknown enum value `HTTPS_PROXY` and reports the member as missing. There is no enum error and no `ValidationException`.
- Bad payload version (18): `BadRequestException` "Unsupported PayloadFormatVersion: 3.0".
- No payload version (19): `BadRequestException` "PayloadFormatVersion is a required parameter for integration".
- Missing required subtype parameter (20): `BadRequestException` "Operation: SQS-SendMessage requires enabling passthrough, or defining all of the following parameters in the parameter mapping: [QueueUrl, MessageBody]".
- SNS subtype (21, 6.1): matches (no SNS subtype): `BadRequestException` "Operation: SNS-Publish is not supported."
- Partial-update validation for subtype parameters (22, 6.2): differs: neither a merge nor a missing-parameter refusal. `BadRequestException` "Invalid integration URI specified" for `update-integration --request-parameters '{"MessageBody":"$request.body"}'` on an SQS-SendMessage integration.
- GetTags on a missing API (23): `NotFoundException` 404 `{"message": "Resource was not found"}` (note the space after the colon, a third body format).
- Tag limits (24, 25; 3.6): tag key max 128, value max 256. Both refused with `BadRequestException` 400 whose `message` is itself a JSON document: `{"message": "1 validation error detected: Value '{...}' at 'addedTags' failed to satisfy constraint: Map keys must satisfy constraint: [Member must have length less than or equal to 128, Member must have length greater than or equal to 1]"}` (values: "Map value must satisfy constraint: [Member must have length less than or equal to 256, ...]"). The CLI prints the nested JSON as the message.
- WebSocket integration type on an HTTP API (26): `BadRequestException` "Currently, an API with a protocol type of HTTP may only be associated with proxy integrations (AWS_PROXY, HTTP_PROXY)".
- 13.4 `error.not-found-text`: matches: `Invalid API identifier specified <account>:<api-id>` holds for v2 GetApi and DeleteApi.
