# APIGW2-05: route selection (apigateway-http.md 14.6)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

Route `GET /slash/` does not exist: 14.1's create-route refused it with `BadRequestException` "Part of the given route key path is empty" (see APIGW2-01/notes.md). Steps 10 and 11 run without it.

## routeKey per step (echoed body)

01 GET /pets/dog/1 -> `GET /pets/dog/1`; 02 /pets/dog/2 -> `GET /pets/dog/{id}`; 03 /pets/cat/1 -> `GET /pets/{proxy+}`; 04 POST /test/5 -> `ANY /{proxy+}`; 05 GET /any -> `GET /any`; 06 POST /any -> `ANY /any`; 07 HEAD /any -> 200, no body (route not visible); 08 /Case -> `GET /Case`; 09 /case -> `ANY /{proxy+}`; 10 /slash/ -> `ANY /{proxy+}` (rawPath `/slash/`); 11 /slash -> `ANY /{proxy+}`; 12 /pets/dog/1/ -> `GET /pets/{proxy+}` (rawPath `/pets/dog/1/`); 13 //pets/dog/1 -> `GET /pets/dog/1` (rawPath `/pets/dog/1`, the double slash collapsed); 14 /enc/a%2Fb -> `ANY /{proxy+}` (rawPath `/enc/a/b`); 15 /enc/a%20b -> `GET /enc/{p}` (rawPath `/enc/a b`); 16 /pets/ -> `GET /pets/{proxy+}`; 17 /noint -> `ANY /{proxy+}`; 18 / -> `ANY /{proxy+}`; 19 OPTIONS /any -> `ANY /any`; 20 /pets/dog/%31 -> `GET /pets/dog/1`; 21 /PETS/dog/1 -> `ANY /{proxy+}`; 22 GET /pets/bird/1 -> `ANY /pets/bird/1`.

## Expected

- 4.3 documented table (01-04): matches.
- Full match on ANY against greedy on the method (22): `ANY /pets/bird/1` wins over `GET /pets/{proxy+}`, so path specificity beats method specificity.
- `/{proxy+}` matches `/` (18) and `/pets/{proxy+}` matches `/pets/` (16): both yes.
- Literal segments case-sensitive (08, 09, 21; 13.4 `route.matching`): matches.
- `/a` and `/a/` distinct (13.4: distinct): matches in effect: `/pets/dog/1/` does not match `GET /pets/dog/1` (12). A route key with a trailing slash cannot be created at all ("Part of the given route key path is empty").
- HEAD matches only HEAD and ANY routes (07; 13.4): n/a (not determinable): `--head` returned 200 with headers only, so the selected route is not visible. A GET or an ANY route served it.
- `//` (13): collapsed: `//pets/dog/1` is routed and echoed as `/pets/dog/1`.
- `%2F` (14): decoded before matching: `/enc/a%2Fb` reaches the function as rawPath `/enc/a/b` and does not match `GET /enc/{p}`. Percent-decoding of `rawPath` (15, 20): `rawPath` is decoded (`/enc/a b`), and `%31` matches the literal `1`.
- Matched route with no target answers 404 (17; 13.4 `route.matching`): differs: `GET /noint` (no target) is skipped; the request reached `ANY /{proxy+}` with 200. A targetless route takes no part in routing (not deployed).
