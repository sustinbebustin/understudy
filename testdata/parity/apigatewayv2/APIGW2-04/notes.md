auto-deploy status after 2 s
auto stage served 200 after 1 s (120 s means never)

# APIGW2-04: deployments and automatic deployment (apigateway-http.md 14.5)

- Date: 2026-11-03
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu)

The two lines above were written by the recipe's own wait loops.

## Expected (13.4 `apigatewayv2.front.answers` deployment rules, 3.5)

- CreateDeployment on an API with no routes (01; 13.4: succeeds): differs: `BadRequestException` 400 "Unable to deploy API because no valid routes exist in this API".
- Stage without a deployment (02; 13.4: 404 Not Found): matches: `404` `{"message":"Not Found"}`, `Content-Type: application/json`, `apigw-requestid`.
- Deploy to dev (03): 201 `{"deploymentId","createdDate","deploymentStatus":"DEPLOYED","autoDeployed":false}`. matches 3.5.
- After the deploy (05): 200 from the function. Undeployed route on a manual stage (06; finding 7): matches: `404` `{"message":"Not Found"}`. The stage serves its deployment snapshot.
- Automatic-deployment delay (13.4 `deploy.auto-latency`: completes before the call returns): about matches: the status read Successfully after 2 s and the stage served 200 after 1 s.
- Explicit `DeploymentId` on an autoDeploy stage (08; 13.4: `BadRequestException`): matches: `BadRequestException` 400 "Deployment ID cannot be set on this stage <account>:<api-id> because AutoDeploy is enabled." (pretty-printed body).
- Deleting a deployment a stage uses (09; 13.4: `BadRequestException`): matches: `BadRequestException` 400 "Active stages pointing to this deployment must be moved or deleted".
- GetDeployments (10): the automatic deployment reads `description "Automatic deployment triggered by changes to the Api configuration"`, `autoDeployed true`. matches 3.5.
