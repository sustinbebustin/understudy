# API Gateway HTTP APIs

understudy serves API Gateway HTTP APIs (`apigatewayv2`): an API, Lambda proxy integrations, routes and stages, and the data plane that invokes the function a route targets. Invoking needs Docker, as Lambda does ([lambda.md](lambda.md)). What each operation supports is in [coverage.md](../coverage.md); this page grows with it.

## A Lambda-backed API

```sh
cat > handler.py <<'EOF'
def handler(event, context):
    return {"statusCode": 200, "body": "hello from " + event["routeKey"]}
EOF
python3 -m zipfile -c function.zip handler.py
aws lambda create-function --function-name hello --runtime python3.13 --handler handler.handler \
  --role arn:aws:iam::000000000000:role/worker --zip-file fileb://function.zip

aws apigatewayv2 create-api --name hello --protocol-type HTTP
# "ApiId": "wfod7wzey6"
aws apigatewayv2 create-integration --api-id wfod7wzey6 --integration-type AWS_PROXY \
  --integration-uri arn:aws:lambda:us-east-1:000000000000:function:hello --payload-format-version 2.0
# "IntegrationId": "ge9nmvg"
aws apigatewayv2 create-route --api-id wfod7wzey6 --route-key 'GET /hello' --target integrations/ge9nmvg
aws apigatewayv2 create-stage --api-id wfod7wzey6 --stage-name '$default' --auto-deploy
```

With `--auto-deploy` the stage serves every change to the API's routes and integrations by the time the call that made it returns; `create-stage` answers the deployment it made (`"LastDeploymentStatusMessage": "Successfully deployed stage with deployment ID '8axhjs'"`).

API Gateway may invoke the function only if its resource policy says so, as on AWS. Until then a request answers `500 {"message":"Internal Server Error"}` and the function does not run, whatever `iam.enforce` says:

```sh
aws lambda add-permission --function-name hello --statement-id apigateway --action lambda:InvokeFunction \
  --principal apigateway.amazonaws.com --source-arn 'arn:aws:execute-api:us-east-1:000000000000:wfod7wzey6/*/*'
```

## Calling the API

An API is reached at `/_understudy/execute-api/<api-id>/<path>` on understudy's endpoint. The `$default` stage serves the paths themselves; a named stage `prod` serves them under `/prod/...`.

```sh
curl -i http://localhost:4566/_understudy/execute-api/wfod7wzey6/hello
# HTTP/1.1 200 OK
# Apigw-Requestid: c4Zzo5MmWjE_qVY=
# Content-Type: text/plain; charset=utf-8
#
# hello from GET /hello
```

The function gets the payload format 2.0 event. Its `rawPath` is the path after the API id, a named stage's segment included. Every answer on this path carries `Content-Security-Policy: sandbox`, since it shares understudy's origin (ADR 0020).

The execute-api host form, `http://<api-id>.execute-api.us-east-1.localhost:4566/hello`, still answers 501.

## What answers NotImplemented for now

Requests understudy cannot serve as AWS would are refused with `NotImplemented` (501), naming what is missing, and change nothing:

- `create-api` with any member but `--name` and `--protocol-type HTTP`, and WebSocket APIs;
- route keys with path variables (`GET /pets/{id}`, `ANY /{proxy+}`) and authorizers; `$default` and literal keys work;
- integrations other than Lambda `AWS_PROXY` with payload format 2.0, and any optional integration member;
- stage members other than `--stage-name` and `--auto-deploy`, so a stage without `--auto-deploy` has no deployment yet and answers `404 {"message":"Not Found"}`;
- every other operation, among them the reads (`get-api`, `get-routes`, ...), updates, deletes and `create-deployment`.

The integration timeout is not applied yet: a request waits for the function however long it runs.
