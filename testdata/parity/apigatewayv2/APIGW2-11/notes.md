# APIGW2-11: the Lambda permission and the source ARN (apigateway-http.md 14.12)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

Session: step 08 was recorded twice. In the first run, $API had no `prod` stage (14.8 creates it and APIGW2-07 was not rerun), so `/prod/perm/1` reached the `$default` stage's `ANY /{proxy+}` route and the `$P-gw` function (200). That capture is invalid; it is kept as `_work/APIGW2-session/first-run-11-08-named-stage.http`. A rerun (later/APIGW2/rerun-11-08.sh: 14.1 verbatim, 14.8's create-stage prod line, 14.12 setup, `try 08` only, 14.17) replaced `08-named-stage.*`.

## Reached the function or got the error

- 01 no permission: `500` `{"message":"Internal Server Error"}`.
- 02 `$default/GET/perm/{id}` (template): 200, reached.
- 03 `$default/GET/perm/1` (concrete): 500.
- 04 `$default/ANY/permany` (ANY literal): 200, reached.
- 05 `$default/GET/permany` (request method): 500.
- 06 `$default/GET/permg/{proxy+}` (greedy template): 200, reached.
- 07 `$default/GET/permg/a/b` (greedy concrete): 500.
- 08 `prod/GET/perm/{id}` (named stage, rerun): 200, reached (stage `prod`, routeKey `GET /perm/{id}`).

## Expected (13.4 `apigatewayv2.lambda.permission`)

- Source ARN `<api-id>/<stage>/<METHOD>/<route path>` with the route key's template and `ANY` as written: matches (02, 04, 06, 08 allowed; 03, 05, 07 denied).
- Denial answers `500` `{"message":"Internal Server Error"}` and the function does not run: matches (01, 03, 05, 07; `Content-Type: application/json`).
