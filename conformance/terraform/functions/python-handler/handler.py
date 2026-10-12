"""Python function the Terraform conformance suite deploys on python3.13.

It answers function URL requests (payload format 2.0) and EventBridge
scheduled events with a small JSON document naming the event it saw.
"""

import json
import os


def handler(event, context):
    if "requestContext" in event and "http" in event["requestContext"]:
        source = "function-url"
    else:
        source = event.get("source", "direct")
    body = {
        "greeting": os.environ.get("GREETING", "hello"),
        "source": source,
        "requestId": context.aws_request_id,
    }
    return {
        "statusCode": 200,
        "headers": {"content-type": "application/json"},
        "body": json.dumps(body),
    }
