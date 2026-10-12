"""Container-image worker of the e2e pipeline.

One handler serves three event sources, told apart by the event's shape:

- an SQS batch from its event source mapping, handled by the Python worker;
- an EventBridge scheduled event, written whole to scheduled/<rule>/<id>.json;
- a function URL request (payload format 2.0), answered with 201, a custom
  header, a cookie and the request event echoed as JSON.
"""

import json
import os

import boto3

import handler as worker

_s3 = boto3.client("s3")


def handler(event, context):
    if "Records" in event:
        return worker.handle_sqs(event, context)
    if event.get("source") == "aws.events":
        return scheduled(event, context)
    if "requestContext" in event:
        return url_request(event)
    raise ValueError("unrecognised event with keys %s" % sorted(event))


def scheduled(event, context):
    rule = event["resources"][0].rsplit("/", 1)[-1]
    _s3.put_object(Bucket=os.environ["BUCKET"], Key="scheduled/%s/%s.json" % (rule, event["id"]),
                   Body=json.dumps(event).encode(), ContentType="application/json")
    worker.log(event="scheduled", rule=rule, id=event["id"], function=context.function_name)
    return None


def url_request(event):
    return {
        "statusCode": 201,
        "headers": {"Content-Type": "application/json", "X-E2e-Worker": "image-worker"},
        "cookies": ["seen=1; Max-Age=60"],
        "body": json.dumps({"event": event}),
        "isBase64Encoded": False,
    }
