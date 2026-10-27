"""Python function behind the HTTP API of the Terraform conformance suite.

It answers API Gateway Lambda proxy requests in either payload format with
a JSON document naming what the request reached, so run.sh can check which
route, stage and payload format served it. Both formats accept the same
response shape (statusCode, headers, body).
"""

import json


def handler(event, context):
    version = event.get("version")
    body = {
        "version": version,
        # 2.0 names the matched route in routeKey, 1.0 its path in resource.
        "route": event.get("routeKey") if version == "2.0" else event.get("resource"),
        "stage": event["requestContext"].get("stage"),
        "pathParameters": event.get("pathParameters"),
        "stageVariables": event.get("stageVariables"),
    }
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json"},
        "body": json.dumps(body),
    }
