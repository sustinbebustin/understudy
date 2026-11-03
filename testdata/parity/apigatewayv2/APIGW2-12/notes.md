# APIGW2-12: AWS_IAM routes (apigateway-http.md 14.13)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

The .trace files and the echoed event bodies of steps 02 and 06 hold the signature and session token: redact per recording-kit.md 2 before sharing.

## Expected (8.3; 4.5 plausible 403 `{"message":"Forbidden"}`)

- Unsigned (01): matches: `403` `{"message":"Forbidden"}`, `Content-Type: application/json`.
- Signed for execute-api (02): 200. The event's `requestContext.authorizer.iam` carries `accessKey`, `accountId`, `callerId`, `cognitoIdentity: null`, `principalOrgId`, `userArn` and `userId`.
- Signed for another service, `lambda` (03): matches: `403` `{"message":"Forbidden"}`.
- Signed for another region, us-west-2 (04): matches: `403` `{"message":"Forbidden"}`.
- Malformed Authorization (05): matches: `403` `{"message":"Forbidden"}`.
- Authenticated caller without `execute-api:Invoke` (07): matches: `403` `{"message":"Forbidden"}`.
- Whether `Authorization` reaches the function (8.3: REST drops it): differs from the REST rule: the function's event keeps `authorization`, `x-amz-date` and `x-amz-security-token` on both the AWS_IAM route (02) and the NONE route (06).
- Signed request to a NONE route (06): 200, and `requestContext.authorizer` has no `iam` (no IAM evaluation on NONE routes; finding 11).
- All four 403 bodies are identical: there is no distinguishing message, and no `x-amzn-ErrorType` header.
