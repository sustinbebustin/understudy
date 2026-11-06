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
| `ui/` | the web UI (item 10): a static page embedded in the binary (`internal/server/ui`) | understudy | the UI page, by navigation; its admin calls pass `refuseBrowser` only with the session secret (below) | `Content-Security-Policy: default-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`, `X-Content-Type-Options: nosniff`, `Cross-Origin-Opener-Policy: same-origin`, `Referrer-Policy: no-referrer` on every answer; no token, since a navigation cannot send one and the files hold no secret |
| `ses/` | the SES verification link `ses/VerificationLink?token=<token>`, the raw operation of `ses` (ses.md 11.3); sent messages are read only through `v1/ses/messages` | understudy's text, which echoes the identity a client wrote | yes, from any page, as AWS serves its own link | `text/plain; charset=utf-8`, `X-Content-Type-Options: nosniff` and exactly one `Content-Security-Policy: sandbox`, set by the operation and imposed by the edge on every answer |

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

## Amendments (implementation, 2026-10-26)

- The UI URL's fragment is `#s=<secret>` (`.../_understudy/ui/#s=<secret>`), read as URL search parameters, in place of the bare `#<secret>` above. The secret is `rand.Text()`: 26 characters of A-Z and 2-7. The session header is `Understudy-UI-Session` (`admin.UISessionHeader`), and `GET /_understudy/v1/ui/session` reports whether a call carries the secret.
- The pass requires exactly one session header and exactly `Sec-Fetch-Site: same-origin`. "The listener's own" Origin is the request's own `<scheme>://<Host>`, `https` when the call came over TLS, compared without regard to ASCII case; two Origin values are refused.
- Known limit: `crossSiteAdmin` refuses any request under `/_understudy/`, the UI's files included, whose `Origin` is not a loopback origin. A page opened at a name that is not loopback, such as an exposed listener's network address, sends that name as `Origin` on its POST, PUT and DELETE calls, so they are refused; whether the page loads there at all is not verified, since a browser may send `Origin` for its module script too, and over plain HTTP at such a name a browser sends no `Sec-Fetch-*` headers, so the page's reads would pass `refuseBrowser` as a program's calls rather than through the secret. Loopback names work fully, over HTTP and over the TLS listener; a remote listener, or one bound to one specific address, is reached through a port forward from a loopback port.
- `crossSiteAdmin` also refuses a cross-site navigation to the UI, such as following a link to it on another site, with its own text that says to paste the printed URL into the address bar. Letting such a navigation through was considered and not done: the page holds no secret and `frame-ancestors 'none'` stops framing, but the UI opens only from its printed URL anyway, and a refusal keeps the rule for `/_understudy/` one rule.

## Amendment (SES verification link, 2026-10-30)

- The `ses/` row no longer reserves the prefix for sent messages: they hold user-written content and are read only through the admin API (`v1/ses/messages`), which refuses browsers (ses.md 11.2, 11.3). `ses/` serves only the verification link, routed as `sns/` is: rule 1 sends `/_understudy/<service>/<Op>` to the raw operation `<Op>` of each service in `internal/edge/claim.go` `serviceLinks`. The link's text is understudy's but echoes an identity a client chose, so its answers are sandboxed and not sniffed like `lambda-code/`'s.
