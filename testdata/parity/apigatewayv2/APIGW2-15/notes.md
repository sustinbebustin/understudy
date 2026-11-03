# APIGW2-15: control-plane IAM denials and resources (apigateway-http.md 14.16)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: n/a (CLI only)

Every step: `403`, `x-amzn-ErrorType: AccessDeniedException`, body `{"message":"User: arn:aws:sts::<account>:assumed-role/us1103-gw-caller/gw15 is not authorized to perform: apigateway:<METHOD> on resource: arn:aws:apigateway:us-east-2::<path> because no identity-based policy allows the apigateway:<METHOD> action"}`.

## Expected (2.3, 8.1)

- Identity of an IAM denial: `AccessDeniedException` 403 with key `message` (it is not modeled for these operations; 2.3 lists it only for portal and domain operations).
- 01 GetApis: GET `/apis`; 02 CreateApi: POST `/apis`. matches (collection form). Step 02 was denied, so no `$P-gw-denied` API was created.
- 03 GetApi: GET `/apis/<id>`; 04 UpdateApi: PATCH `/apis/<id>`. matches.
- 05 DeleteCorsConfiguration: DELETE `/apis/<id>/cors`. matches.
- 06 GetRoutes: GET `/apis/<id>/routes`; 07 GetRoute: GET `/apis/<id>/routes/<route-id>`. matches.
- 08 GetIntegration: GET `/apis/<id>/integrations/<integration-id>`. matches.
- 09 GetStage: GET `/apis/<id>/stages/$default`. matches.
- 10 DeleteAccessLogSettings: DELETE `/apis/<id>/stages/$default/accesslogsettings`. matches.
- 11 DeleteRouteSettings: DELETE `/apis/<id>/stages/$default/routesettings/GET /any`. The route key is written as sent, with its space and `/` unencoded.
- 12 GetDeployment: GET `/apis/<id>/deployments/<deployment-id>`. matches.
- Tag operations (13-15; 8.1: `/tags/<encoded ARN>`): differs: the denial names the tagged API itself, not a `/tags/...` resource. GetTags is `apigateway:GET` on `/apis/<id>`; TagResource and UntagResource are both `apigateway:PATCH` on `/apis/<id>`.
- `/tags/*` policy alone (16-18): not enough: all three are still denied, with the same messages naming `/apis/<id>` (GET, PATCH, PATCH). The tagged API needs its own permission, as for REST APIs.
