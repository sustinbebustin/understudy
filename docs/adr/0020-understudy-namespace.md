# ADR 0020: The /_understudy/ namespace and its browser policy

Status: accepted, 2026-10-26.

## Context

Every path under `/_understudy/` on the main listener is understudy's (ARCHITECTURE.md 4.1 rule 1; no bucket name starts with `_`), and it shares one origin, such as `http://127.0.0.1:4566`, with the admin API. Some of what it serves is written by understudy; some is written by the user's code: the function URL path fallback serves whatever the function returns, and the execute-api path fallback will serve whatever an integration returns once item 6 binds it. On AWS those pages live on origins of their own (`<url-id>.lambda-url.<region>.on.aws`, `lambda-events.md 7.1`; `<api-id>.execute-api.<region>.amazonaws.com`, `apigateway-http.md 4.1`), so a function's page cannot read anything else. Served under `/_understudy/`, a function's page is same-origin with the admin API and, from item 10, the web UI: a browser would let its scripts read their answers.

The admin API already refuses every call a web page makes, same-origin included (`refuseBrowser` in `internal/server/admin`: an `Origin`, or a `Sec-Fetch-Site` other than `none`), and the edge refuses cross-site admin calls before that (`crossSiteAdmin`). Item 10's UI is a page on this origin that must call the admin API, so the namespace needs one written rule for who may serve what, and for how the UI proves it is the UI.

## Decision

Every sub-prefix under `/_understudy/`, on the main listener:

| Sub-prefix | Serves | Content controlled by | Browsers may load it | Headers |
|---|---|---|---|---|
| `v1/` | the admin API (ADR 0017) | understudy | no: `refuseBrowser` and `crossSiteAdmin` refuse any call a page makes; a navigation the user starts (`Sec-Fetch-Site: none`) passes | JSON; `Authorization: Bearer <admin token>` required beyond loopback; no policy added |
| `sns/` | SNS `SigningCertURL`, unsubscribe and confirm links | understudy | yes, from any page, as AWS serves its own links | as the SNS operation writes them (`application/x-x509-ca-cert`, `text/xml`); no policy added |
| `lambda-code/` | `GetFunction` `Code.Location` downloads | the uploader's package bytes, served by understudy | yes, from any page, as a presigned link is | `Content-Type: application/zip`, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`, set by the operation, so a package crafted to sniff as HTML never runs |
| `lambda-url/<url-id>/` | the function URL path fallback | the function | yes, from any page, as on AWS | the function's own, with exactly one `Content-Security-Policy: sandbox` replacing any it set |
| `execute-api/<api-id>/` | the execute-api path fallback | the integration (today understudy's 501) | yes, from any page, as on AWS | exactly one `Content-Security-Policy: sandbox` |
| `ui/` | reserved for the web UI (item 10) | understudy | the UI page, by navigation | decided by item 10; until then the admin part of rule 1 answers |
| `ses/` | reserved for SES | the sender's messages, when it lands | decided by its item, which must sandbox user-written messages as `lambda-url/` is | until then the admin part of rule 1 answers |

- A user-controlled sub-prefix (`lambda-url/`, `execute-api/`) is served with `Content-Security-Policy: sandbox` and no `allow-*` token, in particular no `allow-same-origin`. The edge sets it on every answer of the path fallback, whatever the status, whoever wrote the answer (the function, the raw handler or the edge), and however the header goes out (`internal/edge/sandbox.go`). The page runs in an opaque origin: understudy's answers are cross-origin to it and unreadable, since no admin endpoint sends CORS headers, and the browser marks its requests as another site's (`Origin: null`, `Sec-Fetch-Site: cross-site`), which `crossSiteAdmin` refuses. Without `allow-scripts`, its scripts do not run at all.
- The host forms (`<url-id>.lambda-url.<region>.<base>`, `<api-id>.execute-api...`) get no added policy: each is an origin of its own, as on AWS, and the response passes as written.
- The path fallbacks are claimed only on the main listener; elsewhere the admin part of rule 1 answers them, with no policy added.
- UI secret bootstrap (item 10 implements it): each `serve` draws a secret from `crypto/rand`. It is handed over only in the fragment of the UI URL the banner prints (`.../_understudy/ui/#<secret>`), which browsers never send to a server or in a `Referer`. The UI reads it from `location.hash`, removes it from the address bar, keeps it in memory only, and sends it on each admin call in a custom request header. It is never a cookie, which the browser would attach to any page's request, and never in `localStorage` or `sessionStorage`, which a same-origin page could read. A reload loses it; the banner URL brings it back.
- That rule fits `refuseBrowser` without weakening it: item 10 adds one pass, for a call that is same-origin (`Sec-Fetch-Site: same-origin`, and `Origin`, when sent, the listener's own) and carries the secret (compared in constant time). Every call refused today without the secret is still refused; the admin token, where configured, is still required; `crossSiteAdmin` still runs first. A custom header makes any cross-origin call preflighted, and the admin API approves no preflight.

## Alternatives considered

- `sandbox allow-scripts` (scripts run, origin still opaque): keeps a function's app working on the path form, but its scripts could still send requests to the admin and AWS APIs that only the edge's page checks stand in front of. With no script, no request leaves the page but the loads its markup names. An app that needs scripts has the host form, its own origin, as on AWS. Adding a token later breaks no one; removing one would.
- Serving the fallbacks on a second port, an origin apart: one more listener for every client and container to know, and the path fallback exists for clients that can reach only the one endpoint.
- A cookie or `localStorage` for the UI secret: ambient or readable by any same-origin page, which on this origin includes S3 objects served path style and, without the sandbox, function pages.
- No sandbox, relying on `refuseBrowser` alone: the admin API is protected today, but every same-origin page could still read every other same-origin answer, including the UI's and other functions' pages.

## Consequences

- A page served on the path form runs without scripts, forms or popups. API clients are unaffected: a policy applies to a document, not to an answer `fetch` reads. A browser app needs the host form (`understudy doctor` checks whether `*.localhost` names resolve).
- S3 objects served path style also share understudy's origin and carry no added policy, as S3 sends none (ADR 0013); `refuseBrowser` and, from item 10, the in-memory secret are what keep such a page out of the admin API and the UI.
- A new sub-prefix under `/_understudy/` takes a row here, and one whose content the user controls is sandboxed by the edge, not by its handler.
