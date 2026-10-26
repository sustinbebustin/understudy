# ADR 0019: Several services on one signing name

Status: accepted, 2026-10-26.

## Context

The front door routes a signed request by the service in its SigV4 credential scope (ARCHITECTURE.md 4.1 rule 4), and the registry refused a second service with a signing name another already held (`DuplicateSigningName`). Three pairs of services the roadmap adds share a signing name in AWS, each pair as two Smithy models with their own `aws.auth#sigv4` trait:

- DynamoDB Streams signs `dynamodb` (`dynamodb-streams.md 3.1`). AWS accepts `ListStreams` signed for `dynamodb` and refuses it signed for `streams.dynamodb` (`testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json`, `07-streams-signed-streams.dynamodb.json`; `dynamodb-streams.md 3.2`). Both speak awsJson 1.0 and differ by `X-Amz-Target` prefix (`DynamoDB_20120810`, `DynamoDBStreams_20120810`).
- API Gateway v1 (`com.amazonaws.apigateway#BackplaneControlService`) and v2 (`com.amazonaws.apigatewayv2#ApiGatewayV2`) both sign `apigateway` on one host; both are restJson1, every v2 URI starts `/v2/` and no v1 URI does, and both name operations such as `CreateDeployment` (`apigateway-http.md` finding 3 and 13.1).
- SES v1 (awsQuery at Version `2010-12-01`) and v2 (restJson1 under `/v2/email/`) both sign `ses` (`ses.md 2.1`, `3.4`).

Behind one `AWS_ENDPOINT_URL`, every pair reaches the same port, so the edge must tell each pair apart by the request alone.

## Decision

- `registry.Registry.BySigningName(name)` returns every service of the name, in the order `registry.New` was given them (nil when none; a service with no signing name is never indexed).
- `registry.New` accepts a second service of a name only when a claim rule can tell the pair apart, checked against every earlier service of the name, family by family. Every protocol family both speak must pass its row; a pair with no family in common (awsQuery and REST, awsJson and REST, awsJson and awsQuery) is told apart by the request's shape, which picks the rule:

  | Family both speak | Told apart by | Refused with |
  |---|---|---|
  | awsJson 1.0 or 1.1 (rule 6) | different `JSONTargetPrefix` | `DuplicateJSONTarget` for one non-empty prefix, as before; `DuplicateSigningName` for two empty ones |
  | awsQuery (rule 7) | different `QueryVersion` | `DuplicateQueryAction` for a shared Action at one Version, as before; `DuplicateSigningName` otherwise |
  | restJson1 or restXml (rule 8) | disjoint route tables, below | `OverlappingRouteTables`; `DuplicateSigningName` when one has no route table |
  | rpcv2Cbor (rule 5) | different service `Shape` | `DuplicateSigningName` |

  A service that declares no protocol cannot be told apart from anything (`DuplicateSigningName`). `ref.Raw` is no family: raw operations are claimed by prefix and host rules alone.
- Two REST route tables are disjoint when no request can match a route of both, checked over literal route prefixes: for every pair of routes, one from each table, the first path segments are literals of different values, or one route is `/` and the other starts with a literal. Methods are not compared. So `/v2/...` against `/restapis/...` is disjoint, two tables sharing a first literal overlap whatever follows, and a table with a route starting with a label or a greedy label overlaps every other table that has a route. The refusal names one overlapping pair of routes.
- Rules 6 and 7 are unchanged: the service comes from the target prefix or `(Action, Version)`, and a request signed for another name than that service's is declined.
- Rule 8, for a request signed for a name other than `lambda` and `s3`: the REST services of the name with a route table are tried in registration order, each with the codec of its first REST protocol, and the first match wins; a request none matches is the first one's unknown operation, as a single REST service answers it. A candidate whose codec is not registered is skipped with a reason; with every one skipped the request is unclaimed (rule 9). With no REST service of the name, its first service answers as before (ADR 0013 amendments), and a name no service holds is 501 not emulated. Lambda's and S3's own cases (S3's presigned and SigV2 handling, Lambda's unsigned date path and its decline texts) stay as they are.

## Alternatives considered

- One merged registry service per signing name (`apigateway` holding both models' route tables, `apigateway-http.md 13.1`): the models share operation names (`CreateDeployment`, `GetDeployment`, `TagResource`), and an operation's identity is `service.Name` in bindings, coverage, the journal and IAM, so one of each pair would need a synthetic name no SDK sends, and SES v1 and v2 do not even share a protocol.
- A registry keyed by signing name plus path prefix, or per-path special cases in the edge: works for API Gateway alone, says nothing for awsJson or awsQuery pairs, and repeats the hard-coded `lambda` and `s3` cases this ADR stops adding to.
- Comparing whole route tables (method and every segment) instead of first segments: refuses fewer pairs, but the check would have to reproduce Smithy specificity across two tables; no pair AWS ships needs it, and the first-segment rule is the one a reader can check by eye.

## Consequences

- DynamoDB Streams (item 8), API Gateway v2 (item 6), SES v1 and v2, and a later API Gateway v1 register as their own services, with their own operations, coverage and bindings.
- A model whose REST table would overlap another of its signing name fails at boot with `OverlappingRouteTables` naming both routes, never at request time.
- The order services are wired in matters only for a request no table matches: it is the first REST service's unknown operation.
- Rule 6's not-emulated answer for DynamoDB Streams targets (`dynamodb-streams.md 3.2`) runs only after the codec declines, so a registered Streams claims its targets signed for `dynamodb` first; the entry still answers one signed for `streams.dynamodb` with 501, where AWS answers `InvalidSignatureException`, until item 8 removes it.
