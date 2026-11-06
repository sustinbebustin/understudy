# APIGW2-06: stage selection and the gateway's own answers (apigateway-http.md 14.7)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

## Expected (4.2, 4.5, 13.4 `apigatewayv2.front.answers`)

- A first path segment equal to a stage name selects that stage (01, 03): matches: `/prod/x` goes to stage `prod`, route `GET /x` (rawPath `/prod/x`, stage in rawPath). `/prod/prod/x` goes to stage `prod`, route `GET /prod/x`. `/x` goes to `$default`, `GET /x`.
- Shadowing (4.2): matches: `GET /prod/x` on `$default` is unreachable while stage `prod` exists.
- No route (04): matches: `404` `{"message":"Not Found"}`.
- Bare `/<stage>` (05): `404` `{"message":"Not Found"}`.
- Stage segment case (06, `/PROD/x`): case-sensitive: no stage match, then no `$default` route matched, so `404` Not Found.
- No `$default` stage (07): matches: `404` `{"message":"Not Found"}`.
- Gateway answers carry `Content-Type: application/json` and `apigw-requestid` (13.3 item 8): matches on every 404.
- Unknown API id (08; 13.4: 403 `{"message":"Forbidden"}`): differs: no HTTP answer. The name `0000000000.execute-api.us-east-2.amazonaws.com` does not resolve (curl exit 6, "Could not resolve host"), so there is no wildcard DNS record.
- Disabled default endpoint (09; 13.4: 403 Forbidden): differs: `404` `{"message":"Not Found"}` with `Content-Type: application/json`, 60 s after `--disable-execute-api-endpoint`.
