"""Tests for scripts/distill-parity. Run: uv run --with pytest pytest scripts/tests

Every capture here is synthetic; the account 111122223333 stands in for a
recorded owner account.
"""

import base64
import importlib.machinery
import importlib.util
import json
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent.parent / "distill-parity"
_loader = importlib.machinery.SourceFileLoader("distill_parity", str(SCRIPT))
_spec = importlib.util.spec_from_loader("distill_parity", _loader)
dp = importlib.util.module_from_spec(_spec)
sys.modules["distill_parity"] = dp  # dataclasses resolve annotations through sys.modules
_loader.exec_module(dp)

OWNER = "111122223333"


def unique_id(prefix: str, account: str, tail: int = 7) -> str:
    window = (1 << 47) | (int(account) << 7) | 5
    return prefix + base64.b32encode(window.to_bytes(6, "big") + tail.to_bytes(4, "big")).decode() + "Q"


def log(*messages: str) -> str:
    return "".join(f"2026-10-14 06:30:18,692 - MainThread - {m}\n" for m in messages)


def test_minted_ids_decode_to_the_placeholder_account():
    minted = dp.mint_unique_id("AROA", 3, 21)
    assert len(minted) == 21 and minted.startswith("AROA")
    assert dp.decode_account(minted) == dp.FAKE_ACCOUNT


def test_owner_ids_are_masked_consistently_and_foreign_ids_are_kept():
    role = unique_id("AROA", OWNER)
    foreign = unique_id("AROA", "999988887777")
    session = "44f83428-1081-70c9-3420-b782e60bd470"
    text = (
        f'{{"principalId":"AWS:{role}:{session}","SenderId":"{foreign}:AWS-CLOUDCAST",'
        f'"arn":"arn:aws:sts::{OWNER}:assumed-role/Admin/{session}","sourceIPAddress":"66.219.214.246"}}'
    )
    masker = dp.Masker(OWNER)
    masker.collect(text)
    masked = masker.apply(text)
    for secret in (OWNER, role, session, "66.219.214.246"):
        assert secret not in masked
    assert foreign in masked
    assert masker.apply(text) == masked
    assert len(masked) == len(text.replace(OWNER, dp.FAKE_ACCOUNT))


def test_embedded_json_handles_are_reencoded():
    handle = base64.b64encode(f'{{"taskId":"t1","sourceArn":"arn:aws:sqs:us-east-2:{OWNER}:dlq"}}'.encode()).decode()
    masker = dp.Masker(OWNER)
    masker.collect(handle)
    decoded = base64.b64decode(masker.apply(f'"TaskHandle":"{handle}"').split('"')[3]).decode()
    assert decoded == f'{{"taskId":"t1","sourceArn":"arn:aws:sqs:us-east-2:{dp.FAKE_ACCOUNT}:dlq"}}'


def test_bodies_over_the_limit_become_a_size_and_digest():
    big = b"x" * (dp.BODY_LIMIT + 1)
    assert dp.body_fields(big) == {"body_omitted": {"bytes": len(big), "sha256": dp.hashlib.sha256(big).hexdigest()}}
    assert dp.body_fields(b"\xff\x00") == {"body_base64": "/wA="}
    assert dp.body_fields(b"") == {"body": ""}


def test_credential_check_flags_credentials_but_not_look_alikes():
    assert dp.CREDENTIAL_CHECK.search("ASIA" + "ABCDEFGHIJKLMNOP")
    assert dp.CREDENTIAL_CHECK.search('"sessionToken": "' + "a" * 20)
    assert dp.CREDENTIAL_CHECK.search('{"refreshToken": "x"}')
    assert not dp.CREDENTIAL_CHECK.search('{"__type":"InvalidToken","message":"Invalid Token: x"}')
    assert not dp.CREDENTIAL_CHECK.search("fyfXaKIacQPXEV4bAbCdEfGh")  # a lower-case look-alike in base64


def test_debug_log_drops_credential_refresh_and_keeps_bodies_paired():
    text = log(
        "botocore.endpoint - DEBUG - Making request for OperationModel(name=ReceiveMessage) with params: "
        "{'url_path': '/', 'body': b'{\"QueueUrl\": \"q\"}', 'url': 'https://sqs.us-east-2.amazonaws.com/'}",
        "botocore.endpoint - DEBUG - Making request for OperationModel(name=CreateOAuth2Token) with params: "
        "{'body': b'{\"refreshToken\": \"secret\"}'}",
        "botocore.endpoint - DEBUG - Sending http request: <AWSPreparedRequest stream_output=False, method=POST, "
        "url=https://us-east-2.signin.aws.amazon.com/v1/token, headers={'Content-Length': '25'}>",
        'urllib3.connectionpool - DEBUG - https://us-east-2.signin.aws.amazon.com:443 "POST /v1/token HTTP/1.1" 200 90',
        "botocore.parsers - DEBUG - Response body:\nb'{\"accessToken\": \"secret\"}'",
        "botocore.endpoint - DEBUG - Sending http request: <AWSPreparedRequest stream_output=False, method=POST, "
        "url=https://sqs.us-east-2.amazonaws.com/, headers={'X-Amz-Target': b'AmazonSQS.ReceiveMessage', "
        "'Authorization': b'AWS4-HMAC-SHA256 Credential=ASIAREDACTED', 'Content-Length': '17'}>",
        'urllib3.connectionpool - DEBUG - https://sqs.us-east-2.amazonaws.com:443 "POST / HTTP/1.1" 200 2',
        "botocore.parsers - DEBUG - Response headers: {'Content-Length': '2'}",
        "botocore.parsers - DEBUG - Response body:\nb'{}'",
    )
    assert dp.debug_log_exchanges(text) == [
        {
            "request": {
                "method": "POST",
                "url": "https://sqs.us-east-2.amazonaws.com/",
                "headers": [["X-Amz-Target", "AmazonSQS.ReceiveMessage"], ["Content-Length", "17"]],
                "body": '{"QueueUrl": "q"}',
            },
            "response": {"status": 200, "headers": [["Content-Length", "2"]], "body": "{}"},
        }
    ]


def test_query_protocol_bodies_are_form_encoded():
    text = log(
        "botocore.endpoint - DEBUG - Making request for OperationModel(name=Publish) with params: "
        "{'body': {'Action': 'Publish', 'Message': 'a b&c'}, 'url': 'https://sns.us-east-2.amazonaws.com/'}",
        "botocore.endpoint - DEBUG - Sending http request: <AWSPreparedRequest stream_output=False, method=POST, "
        "url=https://sns.us-east-2.amazonaws.com/, headers={'Content-Length': '30'}>",
    )
    assert dp.debug_log_exchanges(text)[0]["request"]["body"] == "Action=Publish&Message=a+b%26c"


def test_curl_capture_keeps_the_request_and_final_response():
    trace = "* Trying 3.1.4.1:443...\r\n> PUT /k HTTP/1.1\r\n> Host: b.s3.us-east-2.amazonaws.com\r\n> Authorization: x\r\n> Content-Length: 5\r\n> \r\n"
    http = b"HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 200 OK\r\nETag: \"e\"\r\nContent-Length: 0\r\n\r\n"
    assert dp.curl_exchange(http, trace) == {
        "request": {
            "method": "PUT",
            "url": "https://b.s3.us-east-2.amazonaws.com/k",
            "headers": [["Host", "b.s3.us-east-2.amazonaws.com"], ["Content-Length", "5"]],
            "body_unrecorded": {"bytes": 5},
        },
        "response": {"status": 200, "reason": "OK", "headers": [["ETag", '"e"'], ["Content-Length", "0"]], "body": ""},
        "interim_responses": [{"status": 100, "reason": "Continue", "headers": []}],
    }


def test_curl_capture_over_plain_http_keeps_the_http_scheme():
    trace = "* Trying 3.1.4.1:80...\r\n> GET /k HTTP/1.1\r\n> Host: b.s3.us-east-2.amazonaws.com\r\n> \r\n"
    exchange = dp.curl_exchange(b"HTTP/1.1 400 Bad Request\r\n\r\n", trace)
    assert exchange["request"]["url"] == "http://b.s3.us-east-2.amazonaws.com/k"


def test_curl_capture_keeps_the_request_body_recurl_saved():
    trace = "> POST / HTTP/1.1\r\n> Host: kinesis.us-east-2.amazonaws.com\r\n> Content-Length: 13\r\n> \r\n* upload completely sent off: 13 bytes\r\n"
    http = b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}"
    exchange = dp.curl_exchange(http, trace, b'{"Limit": 10}')
    assert exchange["request"]["body"] == '{"Limit": 10}'
    assert "body_unrecorded" not in exchange["request"]


def test_curl_request_body_that_disagrees_with_the_trace_is_refused():
    trace = "> POST / HTTP/1.1\r\n> Host: ssm.us-east-2.amazonaws.com\r\n> Content-Length: 5\r\n> \r\n"
    try:
        dp.curl_exchange(b"HTTP/1.1 200 OK\r\n\r\n", trace, b"{}")
    except dp.DistillError as e:
        assert "2 bytes" in str(e) and "5" in str(e)
    else:
        raise AssertionError("a .req of the wrong length was accepted")


def test_curl_step_reads_the_req_file():
    trace = b"> POST / HTTP/1.1\r\n> Host: states.us-east-2.amazonaws.com\r\n> Content-Length: 2\r\n> \r\n"
    fixture = dp.distill_step("01-x", {".http": b"HTTP/1.1 200 OK\r\n\r\n{}", ".trace": trace, ".req": b"{}"})
    assert fixture["client"] == "curl"
    assert fixture["exchanges"][0]["request"]["body"] == "{}"
    assert ".req" in dp.EXCHANGE_EXTS


def test_queue_items_resolve_to_their_primary_service():
    cases = {
        "R01": "sqs", "KMS-alias": "kms", "SCH": "scheduler", "S3": "s3", "S3-01": "s3", "S3SOL": "s3",
        "IAM-ENF": "iam", "IAM-EVAL-35": "iam", "APIGW1": "apigateway", "APIGW2-08": "apigatewayv2",
        "SSM-22": "ssm", "CWA-23": "cloudwatch", "KIN-01": "kinesis", "SES-28": "ses", "SFN-15": "sfn",
        "ASL-09": "sfn", "SCH-10": "scheduler", "DDBS-05": "dynamodb", "ESMS-02": "lambda", "LDL-01": "lambda",
        "EBB": "events", "SNSH": "sns", "PROTO-05": "dynamodb", "ERR": "sqs",
    }
    assert {item: dp.primary_of(item) for item in cases} == cases
    for unknown in ("R19", "LDL-NN", "TC-01", "ESMS-src", "IAM", "KIN-"):
        assert dp.primary_of(unknown) is None, unknown


def test_hosts_name_the_queue_services():
    def curl(host: str, target: str = "/") -> str:
        return f"> POST {target} HTTP/1.1\n> Host: {host}\n"

    cases = [
        (curl("kinesis.us-east-2.amazonaws.com"), "KIN-01", "kinesis"),
        (curl("ssm.us-east-2.amazonaws.com"), "IAM-ENF", "ssm"),
        (curl("email.us-east-2.amazonaws.com", "/v2/email/outbound-emails"), "IAM-ENF", "ses"),
        (curl("states.us-east-2.amazonaws.com"), "IAM-ENF", "sfn"),
        (curl("apigateway.us-east-2.amazonaws.com", "/restapis"), "IAM-ENF", "apigateway"),
        (curl("apigateway.us-east-2.amazonaws.com", "/v2/apis"), "IAM-ENF", "apigatewayv2"),
        (curl("a1b2c3d4e5.execute-api.us-east-2.amazonaws.com", "/prod/e"), "APIGW2-08", "apigatewayv2"),
        (curl("a1b2c3d4e5.execute-api.us-east-2.amazonaws.com", "/prod/e"), "APIGW1", "apigateway"),
        (curl("ab12cdesqs.execute-api.us-east-2.amazonaws.com", "/prod/e"), "APIGW2-08", "apigatewayv2"),
        (curl("abcdefghij0123456789klmnopqrsts3.lambda-url.us-east-2.on.aws"), "APIGW2-08", "lambda"),
        ("2026-11-03 06:30:18,692 - MainThread - botocore.endpoint - DEBUG - Sending http request: <AWSPreparedRequest "
         "stream_output=False, method=GET, url=https://apigateway.us-east-2.amazonaws.com/v2/apis/x, headers={}>",
         "IAM-ENF", "apigatewayv2"),
    ]
    for text, item, svc in cases:
        assert dp.service_of(text, "01-x", item) == svc, text


def test_cli_step_keeps_exit_and_error_and_drops_parsed_output():
    http = log("awscli.clidriver - DEBUG - CLI version: aws-cli/2").encode() + b"\naws: [ERROR]: An error occurred (X): bad\n"
    fixture = dp.distill_step("01-x", {".http": http, ".out": b"{\n}\nexit=252\n"})
    assert fixture == {"step": "01-x", "client": "aws-cli", "exit": 252, "error": "aws: [ERROR]: An error occurred (X): bad", "stdout": "{\n}", "exchanges": []}


def test_json_puts_header_pairs_and_markers_on_one_line():
    text = dp.to_json({"headers": [["A", "1"]], "body_unrecorded": {"bytes": None}})
    assert '["A", "1"]' in text and '{"bytes": null}' in text
    assert json.loads(text) == {"headers": [["A", "1"]], "body_unrecorded": {"bytes": None}}
