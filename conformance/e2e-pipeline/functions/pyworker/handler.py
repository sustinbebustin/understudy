"""Python worker of the e2e pipeline (python3.13 zip, and the base of the container image).

It is fed by an SQS event source mapping. For each record it unwraps the SNS
envelope when the subscription does not use raw delivery, reads the pipeline
secret, takes a DynamoDB lock whose expiry is the record's SentTimestamp plus
LOCK_TTL_SECONDS, writes the record to the SSE-KMS bucket and prints a JSON
line to its log. A payload that says "fail" raises, which fails the whole
batch, since this worker does not report partial batch failures.

boto3 comes from the Lambda base image and finds understudy through the
AWS_ENDPOINT_URL the emulator sets in the execution environment.
"""

import json
import os

import boto3
from botocore.exceptions import ClientError

_secrets = boto3.client("secretsmanager")
_ddb = boto3.client("dynamodb")
_s3 = boto3.client("s3")


def handler(event, context):
    return handle_sqs(event, context)


def handle_sqs(event, context):
    name = context.function_name
    for record in event["Records"]:
        process(name, record)
    return None


def payload_of(body):
    """The application payload: the SNS Message when the body is an envelope."""
    doc = json.loads(body)
    if isinstance(doc, dict) and doc.get("Type") == "Notification" and "TopicArn" in doc:
        return json.loads(doc["Message"])
    return doc


def process(name, record):
    payload = payload_of(record["body"])
    if payload.get("fail"):
        log(event="failed", id=payload["id"], messageId=record["messageId"], function=name,
            receiveCount=record["attributes"]["ApproximateReceiveCount"])
        raise RuntimeError("the payload asked for a failure: " + payload["id"])
    secret = _secrets.get_secret_value(SecretId=os.environ["SECRET_ID"])
    now = int(record["attributes"]["SentTimestamp"]) // 1000
    expires = now + int(os.environ["LOCK_TTL_SECONDS"])
    lock = {"LockID": "lock#" + payload["id"], "ExpiresAt": str(expires), "Owner": name,
            "MessageId": record["messageId"]}
    try:
        _ddb.put_item(
            TableName=os.environ["LOCK_TABLE"],
            Item={
                "LockID": {"S": lock["LockID"]},
                "ExpiresAt": {"N": lock["ExpiresAt"]},
                "Owner": {"S": name},
                "MessageId": {"S": record["messageId"]},
            },
            ConditionExpression="attribute_not_exists(LockID) OR ExpiresAt < :now",
            ExpressionAttributeValues={":now": {"N": str(now)}},
        )
    except ClientError as err:
        if err.response["Error"]["Code"] != "ConditionalCheckFailedException":
            raise
        log(event="locked", id=payload["id"], messageId=record["messageId"], function=name)
        return
    body = json.dumps({
        "function": name,
        "secretVersionId": secret["VersionId"],
        "record": record,
        "payload": payload,
        "lock": lock,
    })
    _s3.put_object(Bucket=os.environ["BUCKET"], Key="processed/%s/%s.json" % (name, payload["id"]),
                   Body=body.encode(), ContentType="application/json")
    log(event="processed", id=payload["id"], messageId=record["messageId"], function=name,
        receiveCount=record["attributes"]["ApproximateReceiveCount"])


def log(**fields):
    print(json.dumps(fields), flush=True)
