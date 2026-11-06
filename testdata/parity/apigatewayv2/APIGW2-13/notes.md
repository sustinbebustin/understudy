# APIGW2-13: CORS (apigateway-http.md 14.14)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

## Expected (4.6, 3.7)

- Automatic preflight answer "even if there isn't an OPTIONS route" (4.6 first quote): differs: routing comes first. Every OPTIONS request that matched a route (`ANY /any`, `ANY /{proxy+}`, later `OPTIONS /any`) invoked the function, which answered 200 with its echo body (`Content-Type: application/json`). No gateway-generated preflight was seen, so 4.6's second reading holds.
- Preflight status (204 or 200): 200, the function's response.
- Matching preflight (01): the gateway adds `access-control-allow-origin: https://a.example` (the request origin), `access-control-allow-methods: GET,POST`, `access-control-allow-headers: content-type`, `access-control-allow-credentials: true` and `access-control-max-age: 300` to the function's response.
- Non-matching origin (02) or method (03), or OPTIONS without Access-Control-Request-Method (04): 200 from the function with no CORS headers at all.
- GET with a matching origin (05): `access-control-allow-origin: https://a.example`, `access-control-allow-credentials: true` and `access-control-expose-headers: x-e`. The function's own `access-control-allow-origin: https://fn.example` is replaced: matches "ignores CORS headers returned from your backend".
- GET with another origin (06) or none (07): no CORS headers, and the function's own `access-control-allow-origin` is dropped too: matches.
- Preflight on an AWS_IAM route (08): no signature is needed. `OPTIONS /iam` does not match `GET /iam`; it reached `ANY /{proxy+}` (200, CORS headers added).
- With an `OPTIONS /any` route (09): routed to `OPTIONS /any` (function), CORS headers added.
- `AllowOrigins ["*"]` with `AllowCredentials true` (10; 3.7): refused: `BadRequestException` 400 "allow-credentials is not supported if 'allow-origin' is *".
- `MaxAge` 86401 (11; model range -1..86400): differs from the step's intent: the same `BadRequestException` "allow-credentials is not supported if 'allow-origin' is *". UpdateApi merges the new `CorsConfiguration` into the stored one (AllowCredentials true from the setup), so the wildcard check fires first and the MaxAge range is not recorded.
- Body content type of a structured response without `content-type` (05-07, 12 item 10): `text/plain; charset=utf-8`.
