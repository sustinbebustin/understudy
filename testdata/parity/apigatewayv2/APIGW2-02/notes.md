# APIGW2-02: the 429 shape (apigateway-http.md 14.3)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: n/a (CLI only)

## Expected

- `TooManyRequestsException` or `LimitExceededException` on the wire, with `limitType`: n/a (not triggered). All three CreateDeployment calls, about 1 s apart (16:43:23, :24, :25Z), answered 201 `{"deploymentId":..,"createdDate":..,"deploymentStatus":"DEPLOYED","autoDeployed":false}`. No 429 was sent, so the shape is still unrecorded.
- `CreateDeployment` quota "1 request every 5 seconds per account" (3.9): differs: three deployments within 2 s succeeded. The documented quota was not enforced at this rate (it may be a token bucket with a burst).
