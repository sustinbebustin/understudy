"""Signature verification under --sigv4 verify (ADR 0012 pre-tag review amendment).

A request signed with the wrong secret, or with a key understudy holds no secret
for, is refused with each service's own error; the dummy pair test/test and a
12-digit key with the secret test keep working.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from botocore.exceptions import ClientError


def _code(err: pytest.ExceptionInfo[ClientError]) -> str:
    return str(err.value.response["Error"]["Code"])


def test_a_wrong_secret_is_signature_does_not_match_on_s3(
    make_client: Callable[..., Any], make_bucket: Callable[..., str], sigv4_verify: None
) -> None:
    bucket = make_bucket()
    wrong = make_client("s3", secret_key="not-the-secret")
    with pytest.raises(ClientError) as err:
        wrong.list_objects_v2(Bucket=bucket)
    assert _code(err) == "SignatureDoesNotMatch"
    assert err.value.response["ResponseMetadata"]["HTTPStatusCode"] == 403


def test_a_wrong_secret_is_invalid_signature_on_sqs(make_client: Callable[..., Any], sigv4_verify: None) -> None:
    wrong = make_client("sqs", secret_key="not-the-secret")
    with pytest.raises(ClientError) as err:
        wrong.list_queues()
    response = err.value.response
    if wrong.meta.service_model.protocol == "query":
        assert (_code(err), response["ResponseMetadata"]["HTTPStatusCode"]) == ("SignatureDoesNotMatch", 403)
        return
    # SQS JSON is awsQueryCompatible: the body's InvalidSignatureException carries the
    # Query family's code in x-amzn-query-error, which boto3 reports as the code,
    # keeping the body's own identity as QueryErrorCode.
    assert response["ResponseMetadata"]["HTTPStatusCode"] == 400
    assert response["ResponseMetadata"]["HTTPHeaders"]["x-amzn-query-error"] == "SignatureDoesNotMatch;Sender"
    assert _code(err) == "SignatureDoesNotMatch"
    assert response["Error"]["QueryErrorCode"] == "InvalidSignatureException"


def test_a_twelve_digit_key_with_the_dummy_secret_verifies(make_client: Callable[..., Any], sigv4_verify: None) -> None:
    ident = make_client("sts", access_key="444455556666", secret_key="test").get_caller_identity()
    assert ident["Account"] == "444455556666"


def test_an_unknown_key_is_refused(make_client: Callable[..., Any], sigv4_verify: None) -> None:
    unknown = make_client("sts", access_key="AKIAIOSFODNN7EXAMPLE", secret_key="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
    with pytest.raises(ClientError) as err:
        unknown.get_caller_identity()
    assert _code(err) == "InvalidClientTokenId"

    s3 = make_client("s3", access_key="AKIAIOSFODNN7EXAMPLE", secret_key="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
    with pytest.raises(ClientError) as err:
        s3.list_buckets()
    assert _code(err) == "InvalidAccessKeyId"
