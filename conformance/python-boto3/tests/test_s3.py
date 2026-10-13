"""S3 (docs/research/phase1/s3.md): managed transfers, multipart, listing, presigned URLs,
addressing, checksums, conditional requests, SSE-KMS, subresources and notifications."""

from __future__ import annotations

import base64
import hashlib
import io
import json
import random
import struct
import urllib.parse
import zlib
from collections.abc import Callable
from typing import Any

import pytest
from boto3.s3.transfer import TransferConfig
from botocore.config import Config

from support import (
    REGION,
    Admin,
    eventually,
    http_get,
    http_request,
    md5_hex,
    raises,
    signed_http,
    status_of,
    uniq,
    url_host_port,
)

MiB = 1024 * 1024


def payload(size: int, seed: int = 1) -> bytes:
    return random.Random(seed).randbytes(size)


def multipart_etag(data: bytes, part_size: int) -> str:
    digests = b"".join(hashlib.md5(data[i : i + part_size]).digest() for i in range(0, len(data), part_size))
    parts = -(-len(data) // part_size)
    return f"{hashlib.md5(digests).hexdigest()}-{parts}"


def lowered(metadata: dict[str, str]) -> dict[str, str]:
    """User metadata with lower-cased names, for tests that do not examine header-name case.

    test_user_metadata_names_come_back_lower_case covers the case itself.
    """
    return {k.lower(): v for k, v in metadata.items()}


def b64(raw: bytes) -> str:
    return base64.b64encode(raw).decode()


def crc32_b64(data: bytes) -> str:
    return b64(struct.pack(">I", zlib.crc32(data)))


def test_upload_and_download_fileobj_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(300_000)
    s3.upload_fileobj(io.BytesIO(data), bucket, "small.bin")

    out = io.BytesIO()
    s3.download_fileobj(bucket, "small.bin", out)
    assert out.getvalue() == data

    head = s3.head_object(Bucket=bucket, Key="small.bin")
    assert head["ContentLength"] == len(data)
    assert head["ETag"] == f'"{md5_hex(data)}"'


def test_upload_fileobj_of_an_empty_stream(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.upload_fileobj(io.BytesIO(b""), bucket, "empty")
    out = io.BytesIO()
    s3.download_fileobj(bucket, "empty", out)
    assert out.getvalue() == b""
    assert s3.head_object(Bucket=bucket, Key="empty")["ETag"] == f'"{md5_hex(b"")}"'


def test_upload_fileobj_multipart_matches_the_documented_etag(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(12 * MiB)
    config = TransferConfig(multipart_threshold=5 * MiB, multipart_chunksize=5 * MiB, max_concurrency=4)
    s3.upload_fileobj(io.BytesIO(data), bucket, "big.bin", Config=config)

    head = s3.head_object(Bucket=bucket, Key="big.bin")
    assert head["ContentLength"] == len(data)
    assert head["ETag"] == f'"{multipart_etag(data, 5 * MiB)}"'
    assert head["ETag"].endswith('-3"')

    assert s3.get_object(Bucket=bucket, Key="big.bin")["Body"].read() == data


def test_download_fileobj_multipart_reads_ranges(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(11 * MiB, seed=2)
    s3.put_object(Bucket=bucket, Key="big.bin", Body=data)

    out = io.BytesIO()
    config = TransferConfig(multipart_threshold=MiB, multipart_chunksize=3 * MiB, max_concurrency=4)
    s3.download_fileobj(bucket, "big.bin", out, Config=config)
    assert out.getvalue() == data


def test_upload_fileobj_keeps_extra_args(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.upload_fileobj(
        io.BytesIO(b"{}"),
        bucket,
        "doc.json",
        ExtraArgs={"ContentType": "application/json", "Metadata": {"Owner": "Pipeline", "run-id": "42"}},
    )
    head = s3.head_object(Bucket=bucket, Key="doc.json")
    assert head["ContentType"] == "application/json"
    # User metadata names are stored lower-cased.
    assert lowered(head["Metadata"]) == {"owner": "Pipeline", "run-id": "42"}


def test_upload_fileobj_multipart_with_a_sha256_checksum_is_composite(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(11 * MiB, seed=3)
    config = TransferConfig(multipart_threshold=5 * MiB, multipart_chunksize=5 * MiB)
    s3.upload_fileobj(io.BytesIO(data), bucket, "sum.bin", Config=config, ExtraArgs={"ChecksumAlgorithm": "SHA256"})

    raw_parts = b"".join(hashlib.sha256(data[i : i + 5 * MiB]).digest() for i in range(0, len(data), 5 * MiB))
    head = s3.head_object(Bucket=bucket, Key="sum.bin", ChecksumMode="ENABLED")
    assert head["ChecksumSHA256"] == f"{b64(hashlib.sha256(raw_parts).digest())}-3"
    assert head["ChecksumType"] == "COMPOSITE"


def test_upload_fileobj_into_a_missing_bucket(s3: Any) -> None:
    with raises("NoSuchBucket") as exc:
        s3.upload_fileobj(io.BytesIO(b"x"), uniq("conf-nobucket"), "k")
    assert status_of(exc.value) == 404


def test_multipart_lifecycle_with_listing_and_part_get(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    parts_data = [payload(5 * MiB, seed=10), payload(5 * MiB, seed=11), payload(1000, seed=12)]
    upload = s3.create_multipart_upload(Bucket=bucket, Key="mp/obj", Metadata={"kind": "mp"}, ContentType="text/plain")
    upload_id = upload["UploadId"]
    assert upload["Bucket"] == bucket and upload["Key"] == "mp/obj"

    etags = []
    for n, chunk in enumerate(parts_data, start=1):
        part = s3.upload_part(Bucket=bucket, Key="mp/obj", UploadId=upload_id, PartNumber=n, Body=chunk)
        assert part["ETag"] == f'"{md5_hex(chunk)}"'
        etags.append(part["ETag"])

    # In-progress uploads are listed as uploads, never as objects.
    uploads = s3.list_multipart_uploads(Bucket=bucket)
    assert [(u["Key"], u["UploadId"]) for u in uploads["Uploads"]] == [("mp/obj", upload_id)]
    assert "Contents" not in s3.list_objects_v2(Bucket=bucket)

    listed = []
    for page in s3.get_paginator("list_parts").paginate(
        Bucket=bucket, Key="mp/obj", UploadId=upload_id, PaginationConfig={"PageSize": 2}
    ):
        listed.extend(page["Parts"])
    assert [(p["PartNumber"], p["ETag"], p["Size"]) for p in listed] == [
        (1, etags[0], 5 * MiB),
        (2, etags[1], 5 * MiB),
        (3, etags[2], 1000),
    ]

    done = s3.complete_multipart_upload(
        Bucket=bucket,
        Key="mp/obj",
        UploadId=upload_id,
        MultipartUpload={"Parts": [{"PartNumber": n, "ETag": e} for n, e in enumerate(etags, start=1)]},
    )
    assert done["Bucket"] == bucket and done["Key"] == "mp/obj"
    assert done["ETag"] == f'"{multipart_etag(b"".join(parts_data), 5 * MiB)}"'

    head = s3.head_object(Bucket=bucket, Key="mp/obj")
    assert head["ContentLength"] == 10 * MiB + 1000
    assert lowered(head["Metadata"]) == {"kind": "mp"}
    assert head["ContentType"] == "text/plain"

    second = s3.get_object(Bucket=bucket, Key="mp/obj", PartNumber=2)
    assert second["ResponseMetadata"]["HTTPStatusCode"] == 206
    assert second["PartsCount"] == 3
    assert second["ContentRange"] == f"bytes {5 * MiB}-{10 * MiB - 1}/{10 * MiB + 1000}"
    assert second["Body"].read() == parts_data[1]

    # Completing consumes the upload.
    with raises("NoSuchUpload"):
        s3.list_parts(Bucket=bucket, Key="mp/obj", UploadId=upload_id)
    assert "Uploads" not in s3.list_multipart_uploads(Bucket=bucket)


def test_multipart_abort_kills_the_upload(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"x" * 10)
    s3.abort_multipart_upload(Bucket=bucket, Key="k", UploadId=upload_id)
    with raises("NoSuchUpload") as exc:
        s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=2, Body=b"y")
    assert status_of(exc.value) == 404
    with raises("NoSuchUpload"):
        s3.abort_multipart_upload(Bucket=bucket, Key="k", UploadId=upload_id)


def test_multipart_complete_rejects_a_small_non_final_part(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    e1 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"a" * 100)["ETag"]
    e2 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=2, Body=b"b" * 100)["ETag"]
    with raises("EntityTooSmall") as exc:
        s3.complete_multipart_upload(
            Bucket=bucket,
            Key="k",
            UploadId=upload_id,
            MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": e1}, {"PartNumber": 2, "ETag": e2}]},
        )
    assert status_of(exc.value) == 400


def test_multipart_complete_rejects_unordered_parts(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    e1 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=payload(5 * MiB))["ETag"]
    e2 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=2, Body=b"b")["ETag"]
    with raises("InvalidPartOrder"):
        s3.complete_multipart_upload(
            Bucket=bucket,
            Key="k",
            UploadId=upload_id,
            MultipartUpload={"Parts": [{"PartNumber": 2, "ETag": e2}, {"PartNumber": 1, "ETag": e1}]},
        )


def test_multipart_complete_rejects_a_wrong_etag(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"data")
    with raises("InvalidPart"):
        s3.complete_multipart_upload(
            Bucket=bucket,
            Key="k",
            UploadId=upload_id,
            MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": '"00000000000000000000000000000000"'}]},
        )


def test_multipart_complete_accepts_skipped_part_numbers(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    first = payload(5 * MiB, seed=4)
    e1 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=first)["ETag"]
    e3 = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=3, Body=b"tail")["ETag"]
    s3.complete_multipart_upload(
        Bucket=bucket,
        Key="k",
        UploadId=upload_id,
        MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": e1}, {"PartNumber": 3, "ETag": e3}]},
    )
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == first + b"tail"


@pytest.mark.parametrize("part_number", [0, 10001])
def test_upload_part_number_out_of_range(s3: Any, make_bucket: Callable[..., str], part_number: int) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    with raises("InvalidArgument") as exc:
        s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=part_number, Body=b"x")
    assert exc.value.response["Error"]["Message"] == "Part number must be an integer between 1 and 10000, inclusive"


def test_upload_part_overwrites_the_same_part_number(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"first")
    e = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"second")["ETag"]
    s3.complete_multipart_upload(
        Bucket=bucket, Key="k", UploadId=upload_id, MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": e}]}
    )
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == b"second"


def test_upload_part_copy_with_a_range(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    source = payload(6 * MiB, seed=5)
    s3.put_object(Bucket=bucket, Key="src", Body=source)
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="dst")["UploadId"]
    e1 = s3.upload_part_copy(
        Bucket=bucket,
        Key="dst",
        UploadId=upload_id,
        PartNumber=1,
        CopySource={"Bucket": bucket, "Key": "src"},
        CopySourceRange=f"bytes=0-{5 * MiB - 1}",
    )["CopyPartResult"]["ETag"]
    e2 = s3.upload_part_copy(
        Bucket=bucket,
        Key="dst",
        UploadId=upload_id,
        PartNumber=2,
        CopySource={"Bucket": bucket, "Key": "src"},
        CopySourceRange=f"bytes={5 * MiB}-{6 * MiB - 1}",
    )["CopyPartResult"]["ETag"]
    assert e1 == f'"{md5_hex(source[: 5 * MiB])}"'
    s3.complete_multipart_upload(
        Bucket=bucket,
        Key="dst",
        UploadId=upload_id,
        MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": e1}, {"PartNumber": 2, "ETag": e2}]},
    )
    assert s3.get_object(Bucket=bucket, Key="dst")["Body"].read() == source


def test_put_get_head_object_headers(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    body = b"hello object"
    put = s3.put_object(Bucket=bucket, Key="a/b.txt", Body=body, ContentType="text/plain", CacheControl="max-age=60")
    assert put["ETag"] == f'"{md5_hex(body)}"'
    # The SSE-S3 default is applied and reported even when nothing asked for it.
    assert put["ServerSideEncryption"] == "AES256"

    got = s3.get_object(Bucket=bucket, Key="a/b.txt")
    assert got["Body"].read() == body
    assert got["ContentLength"] == len(body)
    assert got["ContentType"] == "text/plain"
    assert got["CacheControl"] == "max-age=60"
    assert got["AcceptRanges"] == "bytes"
    assert got["ETag"] == put["ETag"]
    assert got["LastModified"].microsecond == 0
    assert got["ServerSideEncryption"] == "AES256"
    assert "StorageClass" not in got, "STANDARD is omitted from GET and HEAD"

    head = s3.head_object(Bucket=bucket, Key="a/b.txt")
    assert head["ContentLength"] == len(body)
    assert head["ETag"] == put["ETag"]


def test_default_content_type_is_binary_octet_stream(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    assert s3.head_object(Bucket=bucket, Key="k")["ContentType"] == "binary/octet-stream"


def test_put_object_default_checksum_is_reported(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    body = b"checksum me"
    put = s3.put_object(Bucket=bucket, Key="k", Body=body)
    # boto3 sends CRC32 by default; S3 stores and returns it.
    assert put["ChecksumCRC32"] == crc32_b64(body)
    head = s3.head_object(Bucket=bucket, Key="k", ChecksumMode="ENABLED")
    assert head["ChecksumCRC32"] == crc32_b64(body)


@pytest.mark.parametrize(
    ("algorithm", "digest"),
    [
        ("CRC32", lambda d: struct.pack(">I", zlib.crc32(d))),
        ("SHA1", lambda d: hashlib.sha1(d).digest()),
        ("SHA256", lambda d: hashlib.sha256(d).digest()),
    ],
)
def test_put_object_with_each_checksum_algorithm(
    s3: Any, make_bucket: Callable[..., str], algorithm: str, digest: Callable[[bytes], bytes]
) -> None:
    bucket = make_bucket()
    body = b"flexible checksums " * 100
    put = s3.put_object(Bucket=bucket, Key="k", Body=body, ChecksumAlgorithm=algorithm)
    expected = b64(digest(body))
    assert put[f"Checksum{algorithm}"] == expected
    head = s3.head_object(Bucket=bucket, Key="k", ChecksumMode="ENABLED")
    assert head[f"Checksum{algorithm}"] == expected
    assert s3.get_object(Bucket=bucket, Key="k", ChecksumMode="ENABLED")["Body"].read() == body


def test_put_object_with_a_wrong_checksum_is_a_bad_digest(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    wrong = b64(hashlib.sha256(b"something else").digest())
    with raises("BadDigest") as exc:
        s3.put_object(Bucket=bucket, Key="k", Body=b"payload", ChecksumSHA256=wrong)
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "The SHA256 you specified did not match the calculated checksum."
    with raises("404"):
        s3.head_object(Bucket=bucket, Key="k")


def test_put_object_with_a_wrong_content_md5(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    wrong = b64(hashlib.md5(b"something else").digest())
    with raises("BadDigest"):
        s3.put_object(Bucket=bucket, Key="k", Body=b"payload", ContentMD5=wrong)
    with raises("InvalidDigest"):
        s3.put_object(Bucket=bucket, Key="k", Body=b"payload", ContentMD5="not-base64-of-sixteen-bytes")


def test_trailing_checksum_in_an_aws_chunked_body(s3: Any, endpoint: str, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(100_000, seed=6)
    chunks = [data[:65536], data[65536:]]
    body = b"".join(f"{len(c):x}\r\n".encode() + c + b"\r\n" for c in chunks)
    body += b"0\r\n" + f"x-amz-checksum-crc32:{crc32_b64(data)}\r\n\r\n".encode()
    host, port = url_host_port(endpoint)

    status, headers, resp = signed_http(
        "PUT",
        f"{endpoint}/{bucket}/streamed.bin",
        connect_to=(host, port),
        region=REGION,
        headers={
            "Content-Encoding": "aws-chunked",
            "x-amz-decoded-content-length": str(len(data)),
            "x-amz-trailer": "x-amz-checksum-crc32",
            "Content-Length": str(len(body)),
        },
        body=body,
        trailer_checksum=True,
    )
    assert status == 200, resp
    assert headers["x-amz-checksum-crc32"] == crc32_b64(data)

    head = s3.head_object(Bucket=bucket, Key="streamed.bin", ChecksumMode="ENABLED")
    assert head["ContentLength"] == len(data), "the stored object is the decoded body, not the chunk framing"
    assert head["ChecksumCRC32"] == crc32_b64(data)
    assert "aws-chunked" not in head.get("ContentEncoding", "")
    assert s3.get_object(Bucket=bucket, Key="streamed.bin")["Body"].read() == data


def test_trailing_checksum_mismatch_is_a_bad_digest(endpoint: str, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = b"trailer mismatch"
    body = f"{len(data):x}\r\n".encode() + data + b"\r\n0\r\n" + f"x-amz-checksum-crc32:{crc32_b64(b'other')}\r\n\r\n".encode()
    host, port = url_host_port(endpoint)
    status, _, resp = signed_http(
        "PUT",
        f"{endpoint}/{bucket}/bad",
        connect_to=(host, port),
        region=REGION,
        headers={
            "Content-Encoding": "aws-chunked",
            "x-amz-decoded-content-length": str(len(data)),
            "x-amz-trailer": "x-amz-checksum-crc32",
            "Content-Length": str(len(body)),
        },
        body=body,
        trailer_checksum=True,
    )
    assert status == 400 and b"<Code>BadDigest</Code>" in resp, resp


def test_user_metadata_names_come_back_lower_case(s3: Any, make_bucket: Callable[..., str]) -> None:
    # S3 stores user-defined metadata names in lower case and returns them as x-amz-meta-<lower>;
    # botocore reports the names exactly as the server sent them.
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x", Metadata={"Mixed-Case": "v1", "under_score": "v2"})
    assert s3.head_object(Bucket=bucket, Key="k")["Metadata"] == {"mixed-case": "v1", "under_score": "v2"}
    assert s3.get_object(Bucket=bucket, Key="k")["Metadata"] == {"mixed-case": "v1", "under_score": "v2"}


def test_user_metadata_and_system_headers_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(
        Bucket=bucket,
        Key="k",
        Body=b"x",
        Metadata={"Mixed-Case": "v1", "under_score": "v2"},
        ContentDisposition='attachment; filename="a.txt"',
        ContentEncoding="gzip",
        ContentLanguage="en",
    )
    head = s3.head_object(Bucket=bucket, Key="k")
    assert lowered(head["Metadata"]) == {"mixed-case": "v1", "under_score": "v2"}
    assert head["ContentDisposition"] == 'attachment; filename="a.txt"'
    assert head["ContentEncoding"] == "gzip"
    assert head["ContentLanguage"] == "en"


@pytest.mark.parametrize(
    "key",
    ["with space", "plus+sign", "percent%sign", "unicode-é中", "emoji-\U0001f600", "a//b", "trailing/", "/leading", "dot/../dot", "a@b"],
)
def test_special_key_names_round_trip(s3: Any, make_bucket: Callable[..., str], key: str) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key=key, Body=key.encode())
    assert s3.get_object(Bucket=bucket, Key=key)["Body"].read() == key.encode()
    listed = s3.list_objects_v2(Bucket=bucket)["Contents"]
    assert [o["Key"] for o in listed] == [key]


def test_keys_are_case_sensitive(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="Key", Body=b"x")
    with raises("NoSuchKey"):
        s3.get_object(Bucket=bucket, Key="key")


def test_missing_key_and_bucket_errors(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("NoSuchKey") as exc:
        s3.get_object(Bucket=bucket, Key="nope")
    assert status_of(exc.value) == 404
    assert exc.value.response["Error"]["Message"] == "The specified key does not exist."
    assert exc.value.response["Error"]["Key"] == "nope"

    with raises("404") as head_exc:
        s3.head_object(Bucket=bucket, Key="nope")
    assert status_of(head_exc.value) == 404

    with raises("NoSuchBucket") as bucket_exc:
        s3.get_object(Bucket=uniq("conf-nobucket"), Key="k")
    assert status_of(bucket_exc.value) == 404
    assert "BucketName" in bucket_exc.value.response["Error"]


def test_key_longer_than_1024_bytes_is_rejected(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("KeyTooLongError"):
        s3.put_object(Bucket=bucket, Key="k" * 1025, Body=b"x")
    s3.put_object(Bucket=bucket, Key="k" * 1024, Body=b"x")


def test_range_requests(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = bytes(range(256)) * 8
    s3.put_object(Bucket=bucket, Key="r", Body=data)

    first = s3.get_object(Bucket=bucket, Key="r", Range="bytes=0-9")
    assert first["ResponseMetadata"]["HTTPStatusCode"] == 206
    assert first["ContentRange"] == f"bytes 0-9/{len(data)}"
    assert first["ContentLength"] == 10
    assert first["Body"].read() == data[:10]

    assert s3.get_object(Bucket=bucket, Key="r", Range="bytes=-5")["Body"].read() == data[-5:]
    assert s3.get_object(Bucket=bucket, Key="r", Range=f"bytes={len(data) - 3}-")["Body"].read() == data[-3:]
    clipped = s3.get_object(Bucket=bucket, Key="r", Range=f"bytes=2040-{len(data) + 500}")
    assert clipped["ContentRange"] == f"bytes 2040-{len(data) - 1}/{len(data)}"


def test_unsatisfiable_range(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="r", Body=b"12345678")
    with raises("InvalidRange") as exc:
        s3.get_object(Bucket=bucket, Key="r", Range="bytes=1024-4096")
    assert status_of(exc.value) == 416
    assert exc.value.response["Error"]["Message"] == "The requested range is not satisfiable"
    assert exc.value.response["Error"]["ActualObjectSize"] == "8"
    assert exc.value.response["Error"]["RangeRequested"] == "bytes=1024-4096"


def test_conditional_reads(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    etag = s3.put_object(Bucket=bucket, Key="c", Body=b"conditional")["ETag"]

    assert s3.get_object(Bucket=bucket, Key="c", IfMatch=etag)["Body"].read() == b"conditional"
    with raises("PreconditionFailed") as exc:
        s3.get_object(Bucket=bucket, Key="c", IfMatch='"deadbeef"')
    assert status_of(exc.value) == 412
    assert exc.value.response["Error"]["Condition"] == "If-Match"

    with raises("304") as not_modified:
        s3.get_object(Bucket=bucket, Key="c", IfNoneMatch=etag)
    assert status_of(not_modified.value) == 304
    # Quotes are optional when matching.
    with raises("304"):
        s3.head_object(Bucket=bucket, Key="c", IfNoneMatch=etag.strip('"'))
    assert s3.get_object(Bucket=bucket, Key="c", IfNoneMatch='"deadbeef"')["Body"].read() == b"conditional"


def test_conditional_writes(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    first = s3.put_object(Bucket=bucket, Key="lock", Body=b"one", IfNoneMatch="*")
    with raises("PreconditionFailed") as exc:
        s3.put_object(Bucket=bucket, Key="lock", Body=b"two", IfNoneMatch="*")
    assert status_of(exc.value) == 412
    assert exc.value.response["Error"]["Condition"] == "If-None-Match"
    assert s3.get_object(Bucket=bucket, Key="lock")["Body"].read() == b"one"

    s3.put_object(Bucket=bucket, Key="lock", Body=b"three", IfMatch=first["ETag"])
    with raises("PreconditionFailed"):
        s3.put_object(Bucket=bucket, Key="lock", Body=b"four", IfMatch=first["ETag"])
    with raises("NoSuchKey"):
        s3.put_object(Bucket=bucket, Key="absent", Body=b"x", IfMatch='"deadbeef"')
    assert s3.get_object(Bucket=bucket, Key="lock")["Body"].read() == b"three"


def test_conditional_multipart_complete(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"existing")
    upload_id = s3.create_multipart_upload(Bucket=bucket, Key="k")["UploadId"]
    etag = s3.upload_part(Bucket=bucket, Key="k", UploadId=upload_id, PartNumber=1, Body=b"new")["ETag"]
    with raises("PreconditionFailed"):
        s3.complete_multipart_upload(
            Bucket=bucket,
            Key="k",
            UploadId=upload_id,
            MultipartUpload={"Parts": [{"PartNumber": 1, "ETag": etag}]},
            IfNoneMatch="*",
        )
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == b"existing"


def test_delete_object_is_idempotent(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    assert s3.delete_object(Bucket=bucket, Key="k")["ResponseMetadata"]["HTTPStatusCode"] == 204
    assert s3.delete_object(Bucket=bucket, Key="k")["ResponseMetadata"]["HTTPStatusCode"] == 204
    with raises("NoSuchBucket"):
        s3.delete_object(Bucket=uniq("conf-nobucket"), Key="k")


def test_delete_objects_reports_deleted_and_missing_keys(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    for k in ("a", "b", "c"):
        s3.put_object(Bucket=bucket, Key=k, Body=b"x")
    out = s3.delete_objects(Bucket=bucket, Delete={"Objects": [{"Key": "a"}, {"Key": "b"}, {"Key": "never-existed"}]})
    assert sorted(d["Key"] for d in out["Deleted"]) == ["a", "b", "never-existed"]
    assert not out.get("Errors")
    assert [o["Key"] for o in s3.list_objects_v2(Bucket=bucket)["Contents"]] == ["c"]

    quiet = s3.delete_objects(Bucket=bucket, Delete={"Objects": [{"Key": "c"}], "Quiet": True})
    assert "Deleted" not in quiet
    assert "Contents" not in s3.list_objects_v2(Bucket=bucket)


def test_copy_object_copies_data_and_metadata(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="src", Body=b"copy me", Metadata={"a": "1"}, ContentType="text/plain")
    out = s3.copy_object(Bucket=bucket, Key="dst", CopySource={"Bucket": bucket, "Key": "src"})
    assert out["CopyObjectResult"]["ETag"] == f'"{md5_hex(b"copy me")}"'
    head = s3.head_object(Bucket=bucket, Key="dst")
    assert lowered(head["Metadata"]) == {"a": "1"}
    assert head["ContentType"] == "text/plain"
    assert s3.get_object(Bucket=bucket, Key="dst")["Body"].read() == b"copy me"


def test_copy_object_metadata_directive_replace(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="src", Body=b"x", Metadata={"old": "1"}, ContentType="text/plain")
    s3.copy_object(
        Bucket=bucket,
        Key="dst",
        CopySource={"Bucket": bucket, "Key": "src"},
        MetadataDirective="REPLACE",
        Metadata={"new": "2"},
    )
    head = s3.head_object(Bucket=bucket, Key="dst")
    assert lowered(head["Metadata"]) == {"new": "2"}
    assert head["ContentType"] == "binary/octet-stream"


def test_copy_object_onto_itself_needs_a_change(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    with raises("InvalidRequest") as exc:
        s3.copy_object(Bucket=bucket, Key="k", CopySource={"Bucket": bucket, "Key": "k"})
    assert exc.value.response["Error"]["Message"] == (
        "This copy request is illegal because it is trying to copy an object to itself without changing the "
        "object's metadata, storage class, website redirect location or encryption attributes."
    )
    s3.copy_object(
        Bucket=bucket,
        Key="k",
        CopySource={"Bucket": bucket, "Key": "k"},
        MetadataDirective="REPLACE",
        Metadata={"changed": "yes"},
    )
    assert lowered(s3.head_object(Bucket=bucket, Key="k")["Metadata"]) == {"changed": "yes"}


def test_copy_object_from_a_missing_key(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("NoSuchKey"):
        s3.copy_object(Bucket=bucket, Key="dst", CopySource={"Bucket": bucket, "Key": "missing"})


def test_copy_of_a_multipart_object_gets_a_content_md5_etag(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    data = payload(6 * MiB, seed=7)
    s3.upload_fileobj(
        io.BytesIO(data), bucket, "src", Config=TransferConfig(multipart_threshold=5 * MiB, multipart_chunksize=5 * MiB)
    )
    assert s3.head_object(Bucket=bucket, Key="src")["ETag"].endswith('-2"')
    out = s3.copy_object(Bucket=bucket, Key="dst", CopySource={"Bucket": bucket, "Key": "src"})
    assert out["CopyObjectResult"]["ETag"] == f'"{md5_hex(data)}"'


def test_object_tags(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="t", Body=b"x", Tagging="env=test&team=pipeline")
    got = s3.get_object(Bucket=bucket, Key="t")
    assert got["TagCount"] == 2
    tags = {t["Key"]: t["Value"] for t in s3.get_object_tagging(Bucket=bucket, Key="t")["TagSet"]}
    assert tags == {"env": "test", "team": "pipeline"}

    s3.put_object_tagging(Bucket=bucket, Key="t", Tagging={"TagSet": [{"Key": "only", "Value": "one"}]})
    assert s3.get_object_tagging(Bucket=bucket, Key="t")["TagSet"] == [{"Key": "only", "Value": "one"}]

    # Overwriting the object clears its tags.
    s3.put_object(Bucket=bucket, Key="t", Body=b"y")
    assert s3.get_object_tagging(Bucket=bucket, Key="t")["TagSet"] == []
    assert "TagCount" not in s3.head_object(Bucket=bucket, Key="t")

    s3.put_object_tagging(Bucket=bucket, Key="t", Tagging={"TagSet": [{"Key": "a", "Value": "b"}]})
    assert s3.delete_object_tagging(Bucket=bucket, Key="t")["ResponseMetadata"]["HTTPStatusCode"] == 204
    assert s3.get_object_tagging(Bucket=bucket, Key="t")["TagSet"] == []


def test_storage_class_round_trip_and_validation(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="ia", Body=b"x", StorageClass="STANDARD_IA")
    assert s3.head_object(Bucket=bucket, Key="ia")["StorageClass"] == "STANDARD_IA"
    with raises("InvalidStorageClass"):
        s3.put_object(Bucket=bucket, Key="bad", Body=b"x", StorageClass="OUTPOSTS")


def put_keys(s3: Any, bucket: str, keys: list[str]) -> None:
    for k in keys:
        s3.put_object(Bucket=bucket, Key=k, Body=k.encode())


def test_list_objects_v2_paginator_walks_every_key_in_order(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    keys = [f"data/{i:03d}" for i in range(25)]
    put_keys(s3, bucket, keys)

    pages = list(s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, PaginationConfig={"PageSize": 10}))
    assert [len(p["Contents"]) for p in pages] == [10, 10, 5]
    assert [p["KeyCount"] for p in pages] == [10, 10, 5]
    assert [o["Key"] for p in pages for o in p["Contents"]] == keys
    assert [p["IsTruncated"] for p in pages] == [True, True, False]


def test_list_objects_v2_manual_continuation(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a", "b", "c", "d", "e"])
    first = s3.list_objects_v2(Bucket=bucket, MaxKeys=2)
    assert [o["Key"] for o in first["Contents"]] == ["a", "b"]
    assert first["IsTruncated"] is True
    assert first["MaxKeys"] == 2
    token = first["NextContinuationToken"]

    second = s3.list_objects_v2(Bucket=bucket, MaxKeys=2, ContinuationToken=token)
    assert [o["Key"] for o in second["Contents"]] == ["c", "d"]
    assert second["ContinuationToken"] == token
    last = s3.list_objects_v2(Bucket=bucket, MaxKeys=2, ContinuationToken=second["NextContinuationToken"])
    assert [o["Key"] for o in last["Contents"]] == ["e"]
    assert last["IsTruncated"] is False
    assert "NextContinuationToken" not in last


def test_list_objects_v2_rejects_a_made_up_continuation_token(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a"])
    with raises("InvalidArgument") as exc:
        s3.list_objects_v2(Bucket=bucket, ContinuationToken="not-a-real-token")
    assert exc.value.response["Error"]["ArgumentName"] == "continuation-token"
    assert exc.value.response["Error"]["Message"] == "The continuation token provided is incorrect"


def test_list_objects_v2_orders_keys_by_utf8_bytes(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    keys = ["test_2", "test_11", "test_1", "test_10", "Test_0", "test_0", "é", "z"]
    put_keys(s3, bucket, keys)
    listed = [o["Key"] for o in s3.list_objects_v2(Bucket=bucket)["Contents"]]
    assert listed == sorted(keys, key=lambda k: k.encode())


def test_list_objects_v2_prefix_delimiter_and_common_prefixes(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a/1", "a/2", "a/sub/3", "b/1", "c", "d.txt"])

    top = s3.list_objects_v2(Bucket=bucket, Delimiter="/")
    assert [p["Prefix"] for p in top["CommonPrefixes"]] == ["a/", "b/"]
    assert [o["Key"] for o in top["Contents"]] == ["c", "d.txt"]
    assert top["KeyCount"] == 4

    inner = s3.list_objects_v2(Bucket=bucket, Prefix="a/", Delimiter="/")
    assert [p["Prefix"] for p in inner["CommonPrefixes"]] == ["a/sub/"]
    assert [o["Key"] for o in inner["Contents"]] == ["a/1", "a/2"]
    assert inner["Prefix"] == "a/"
    assert inner["Delimiter"] == "/"

    # A prefix without a trailing slash still collapses at the next delimiter.
    partial = s3.list_objects_v2(Bucket=bucket, Prefix="a/s", Delimiter="/")
    assert [p["Prefix"] for p in partial["CommonPrefixes"]] == ["a/sub/"]


def test_list_objects_v2_counts_common_prefixes_against_max_keys(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a/1", "a/2", "b/1", "b/2", "c"])
    pages = list(
        s3.get_paginator("list_objects_v2").paginate(Bucket=bucket, Delimiter="/", PaginationConfig={"PageSize": 2})
    )
    entries = [[*(p["Prefix"] for p in page.get("CommonPrefixes", [])), *(o["Key"] for o in page.get("Contents", []))] for page in pages]
    assert entries == [["a/", "b/"], ["c"]]


def test_list_objects_v2_start_after(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a", "b", "c"])
    out = s3.list_objects_v2(Bucket=bucket, StartAfter="a")
    assert [o["Key"] for o in out["Contents"]] == ["b", "c"]
    assert out["StartAfter"] == "a"


def test_list_objects_v2_on_an_empty_bucket_and_missing_bucket(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    out = s3.list_objects_v2(Bucket=bucket)
    assert out["KeyCount"] == 0 and out["IsTruncated"] is False
    assert "Contents" not in out
    with raises("NoSuchBucket"):
        s3.list_objects_v2(Bucket=uniq("conf-nobucket"))


def test_list_objects_v2_entry_shape(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"12345")
    [entry] = s3.list_objects_v2(Bucket=bucket)["Contents"]
    assert entry["Size"] == 5
    assert entry["ETag"] == f'"{md5_hex(b"12345")}"'
    assert entry["StorageClass"] == "STANDARD"
    assert "Owner" not in entry
    [with_owner] = s3.list_objects_v2(Bucket=bucket, FetchOwner=True)["Contents"]
    assert with_owner["Owner"]["ID"]


def test_list_objects_v1_marker_and_next_marker(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    put_keys(s3, bucket, ["a/1", "a/2", "b/1", "c"])
    first = s3.list_objects(Bucket=bucket, MaxKeys=1, Delimiter="/")
    assert [p["Prefix"] for p in first["CommonPrefixes"]] == ["a/"]
    assert first["IsTruncated"] is True
    assert first["NextMarker"] == "a/"

    # Without a delimiter a truncated page carries no NextMarker; the client continues from the last key.
    plain = s3.list_objects(Bucket=bucket, MaxKeys=2)
    assert plain["IsTruncated"] is True and "NextMarker" not in plain
    rest = s3.list_objects(Bucket=bucket, Marker=plain["Contents"][-1]["Key"])
    assert [o["Key"] for o in rest["Contents"]] == ["b/1", "c"]
    assert all("Owner" in o for o in plain["Contents"]), "v1 listings always carry Owner"

    walked = [o["Key"] for page in s3.get_paginator("list_objects").paginate(Bucket=bucket, PaginationConfig={"PageSize": 3}) for o in page["Contents"]]
    assert walked == ["a/1", "a/2", "b/1", "c"]


def test_listing_never_shows_in_progress_multipart_uploads(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.create_multipart_upload(Bucket=bucket, Key="pending")
    assert "Contents" not in s3.list_objects_v2(Bucket=bucket)


@pytest.fixture(params=["s3", "s3v4"])
def presign(request: pytest.FixtureRequest, make_client: Callable[..., Any]) -> Any:
    """A client that signs query-string URLs with SigV2 ("s3") or SigV4 ("s3v4").

    boto3 still signs presigned URLs for us-east-1 with SigV2 unless told otherwise, so a
    local endpoint has to serve both forms.
    """
    return make_client("s3", config=Config(signature_version=request.param))


def test_presigned_get(s3: Any, presign: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="dir/file name.txt", Body=b"presigned body", ContentType="text/plain")
    url = presign.generate_presigned_url("get_object", Params={"Bucket": bucket, "Key": "dir/file name.txt"}, ExpiresIn=300)
    status, headers, body = http_get(url)
    assert status == 200
    assert body == b"presigned body"
    assert headers["content-type"] == "text/plain"
    assert headers["etag"] == f'"{md5_hex(b"presigned body")}"'


def test_presigned_get_applies_response_header_overrides(s3: Any, presign: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x", ContentType="text/plain")
    url = presign.generate_presigned_url(
        "get_object",
        Params={
            "Bucket": bucket,
            "Key": "k",
            "ResponseContentDisposition": 'attachment; filename="renamed.txt"',
            "ResponseContentType": "application/x-custom",
        },
        ExpiresIn=300,
    )
    status, headers, _ = http_get(url)
    assert status == 200
    assert headers["content-disposition"] == 'attachment; filename="renamed.txt"'
    assert headers["content-type"] == "application/x-custom"


def test_presigned_put_then_get(s3: Any, presign: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    url = presign.generate_presigned_url(
        "put_object", Params={"Bucket": bucket, "Key": "uploaded", "ContentType": "text/csv"}, ExpiresIn=300
    )
    status, _, _ = http_request("PUT", url, data=b"a,b\n1,2\n", headers={"Content-Type": "text/csv"})
    assert status == 200
    got = s3.get_object(Bucket=bucket, Key="uploaded")
    assert got["Body"].read() == b"a,b\n1,2\n"
    assert got["ContentType"] == "text/csv"
    # S3 stores CRC64NVME for a write that carried no checksum.
    assert s3.head_object(Bucket=bucket, Key="uploaded", ChecksumMode="ENABLED").get("ChecksumType") == "FULL_OBJECT"


def test_presigned_head(s3: Any, presign: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"12345")
    url = presign.generate_presigned_url("head_object", Params={"Bucket": bucket, "Key": "k"}, ExpiresIn=300)
    status, headers, body = http_request("HEAD", url)
    assert status == 200 and body == b""
    assert headers["content-length"] == "5"


def test_presigned_get_of_a_missing_key_is_a_no_such_key_document(presign: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    url = presign.generate_presigned_url("get_object", Params={"Bucket": bucket, "Key": "missing"}, ExpiresIn=300)
    status, headers, body = http_get(url)
    assert status == 404
    assert b"<Code>NoSuchKey</Code>" in body
    assert headers["content-type"].startswith("application/xml")


def test_presigned_url_expires_on_the_emulator_clock(
    s3: Any, presign: Any, make_bucket: Callable[..., str], clock: Admin, sigv4_verify: None
) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    url = presign.generate_presigned_url("get_object", Params={"Bucket": bucket, "Key": "k"}, ExpiresIn=60)
    assert http_get(url)[0] == 200
    clock.advance(61)
    status, _, body = http_get(url)
    assert status == 403
    assert b"<Code>AccessDenied</Code>" in body and b"Request has expired" in body


def test_presigned_url_with_a_tampered_signature_is_rejected(
    s3: Any, presign: Any, make_bucket: Callable[..., str], sigv4_verify: None
) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    url = presign.generate_presigned_url("get_object", Params={"Bucket": bucket, "Key": "k"}, ExpiresIn=300)
    parts = urllib.parse.urlsplit(url)
    query = urllib.parse.parse_qs(parts.query, keep_blank_values=True)
    name = "X-Amz-Signature" if "X-Amz-Signature" in query else "Signature"
    query[name] = ["A" * 8 + query[name][0][8:]]
    tampered = parts._replace(query=urllib.parse.urlencode(query, doseq=True)).geturl()
    status, _, body = http_get(tampered)
    assert status == 403
    assert b"<Code>SignatureDoesNotMatch</Code>" in body


@pytest.mark.parametrize("base", ["localhost", "s3.localhost"])
def test_virtual_hosted_addressing(endpoint: str, s3: Any, make_bucket: Callable[..., str], base: str) -> None:
    bucket = make_bucket()
    host, port = url_host_port(endpoint)
    url = f"http://{bucket}.{base}:{port}/vh/key.txt"

    status, _, body = signed_http("PUT", url, connect_to=(host, port), region=REGION, body=b"virtual hosted")
    assert status == 200, body
    assert s3.get_object(Bucket=bucket, Key="vh/key.txt")["Body"].read() == b"virtual hosted"

    status, headers, body = signed_http("GET", url, connect_to=(host, port), region=REGION)
    assert (status, body) == (200, b"virtual hosted")

    listing_status, _, listing = signed_http(
        "GET", f"http://{bucket}.{base}:{port}/?list-type=2&prefix=vh/", connect_to=(host, port), region=REGION
    )
    assert listing_status == 200 and b"<Key>vh/key.txt</Key>" in listing


def test_path_and_virtual_hosted_requests_see_the_same_bucket(endpoint: str, s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    host, port = url_host_port(endpoint)
    s3.put_object(Bucket=bucket, Key="k", Body=b"via sdk")
    status, _, body = signed_http(
        "GET", f"http://{bucket}.s3.localhost:{port}/k", connect_to=(host, port), region=REGION
    )
    assert (status, body) == (200, b"via sdk")


def test_bucket_create_head_list_delete(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    head = s3.head_bucket(Bucket=bucket)
    assert head["BucketRegion"] == REGION
    assert bucket in [b["Name"] for b in s3.list_buckets()["Buckets"]]
    s3.delete_bucket(Bucket=bucket)
    with raises("404") as exc:
        s3.head_bucket(Bucket=bucket)
    assert status_of(exc.value) == 404
    assert bucket not in [b["Name"] for b in s3.list_buckets().get("Buckets", [])]


def test_list_buckets_of_an_account_with_no_buckets_has_an_empty_list(make_client: Callable[..., Any]) -> None:
    fresh_account = make_client("s3", access_key="111111111111")
    out = fresh_account.list_buckets()
    assert out["Buckets"] == []
    assert out["Owner"]["ID"]


def test_bucket_names_are_global_across_accounts(s3: Any, make_client: Callable[..., Any], make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    other_account = make_client("s3", access_key="222222222222")
    with raises("BucketAlreadyExists") as exc:
        other_account.create_bucket(Bucket=bucket)
    assert status_of(exc.value) == 409
    assert bucket not in [b["Name"] for b in other_account.list_buckets().get("Buckets", [])]


def test_create_bucket_twice_in_us_east_1_succeeds(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.create_bucket(Bucket=bucket)


def test_create_bucket_with_a_location_constraint(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket(CreateBucketConfiguration={"LocationConstraint": "us-west-2"})
    assert s3.get_bucket_location(Bucket=bucket)["LocationConstraint"] == "us-west-2"


def test_us_east_1_bucket_has_an_empty_location(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    assert not s3.get_bucket_location(Bucket=bucket).get("LocationConstraint")


def test_us_east_1_location_constraint_is_invalid(s3: Any) -> None:
    with raises("InvalidLocationConstraint") as exc:
        s3.create_bucket(Bucket=uniq("conf-loc"), CreateBucketConfiguration={"LocationConstraint": "us-east-1"})
    assert status_of(exc.value) == 400


@pytest.mark.parametrize("name", ["UpperCase-bucket", "ab", "has_underscore", "-leading-dash", "double..dot"])
def test_invalid_bucket_names(s3: Any, name: str) -> None:
    with raises("InvalidBucketName") as exc:
        s3.create_bucket(Bucket=name)
    assert status_of(exc.value) == 400


def test_delete_non_empty_bucket(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    with raises("BucketNotEmpty") as exc:
        s3.delete_bucket(Bucket=bucket)
    assert status_of(exc.value) == 409
    assert exc.value.response["Error"]["Message"] == "The bucket you tried to delete is not empty"


def test_fresh_bucket_defaults(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    ownership = s3.get_bucket_ownership_controls(Bucket=bucket)["OwnershipControls"]["Rules"]
    assert ownership == [{"ObjectOwnership": "BucketOwnerEnforced"}]
    assert s3.get_public_access_block(Bucket=bucket)["PublicAccessBlockConfiguration"] == {
        "BlockPublicAcls": True,
        "IgnorePublicAcls": True,
        "BlockPublicPolicy": True,
        "RestrictPublicBuckets": True,
    }
    rule = s3.get_bucket_encryption(Bucket=bucket)["ServerSideEncryptionConfiguration"]["Rules"][0]
    assert rule["ApplyServerSideEncryptionByDefault"]["SSEAlgorithm"] == "AES256"
    assert "Status" not in s3.get_bucket_versioning(Bucket=bucket)
    assert s3.get_bucket_accelerate_configuration(Bucket=bucket).get("Status") is None
    assert s3.get_bucket_request_payment(Bucket=bucket)["Payer"] == "BucketOwner"
    assert "LoggingEnabled" not in s3.get_bucket_logging(Bucket=bucket)
    notification = s3.get_bucket_notification_configuration(Bucket=bucket)
    assert not any(k.endswith("Configurations") for k in notification)
    acl = s3.get_bucket_acl(Bucket=bucket)
    assert [g["Permission"] for g in acl["Grants"]] == ["FULL_CONTROL"]


@pytest.mark.parametrize(
    ("operation", "code", "message"),
    [
        ("get_bucket_policy", "NoSuchBucketPolicy", "The bucket policy does not exist"),
        ("get_bucket_tagging", "NoSuchTagSet", "The TagSet does not exist"),
        ("get_bucket_cors", "NoSuchCORSConfiguration", "The CORS configuration does not exist"),
        ("get_bucket_website", "NoSuchWebsiteConfiguration", "The specified bucket does not have a website configuration"),
        ("get_bucket_lifecycle_configuration", "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist"),
        ("get_bucket_replication", "ReplicationConfigurationNotFoundError", "The replication configuration was not found"),
        (
            "get_object_lock_configuration",
            "ObjectLockConfigurationNotFoundError",
            "Object Lock configuration does not exist for this bucket",
        ),
    ],
)
def test_unset_subresources_fail_with_the_exact_not_configured_error(
    s3: Any, make_bucket: Callable[..., str], operation: str, code: str, message: str
) -> None:
    bucket = make_bucket()
    with raises(code) as exc:
        getattr(s3, operation)(Bucket=bucket)
    assert status_of(exc.value) == 404
    assert exc.value.response["Error"]["Message"] == message
    assert exc.value.response["Error"]["BucketName"] == bucket


def test_deleted_default_subresources_report_not_found(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.delete_public_access_block(Bucket=bucket)
    with raises("NoSuchPublicAccessBlockConfiguration"):
        s3.get_public_access_block(Bucket=bucket)
    s3.delete_bucket_ownership_controls(Bucket=bucket)
    with raises("OwnershipControlsNotFoundError"):
        s3.get_bucket_ownership_controls(Bucket=bucket)


def test_unset_subresource_on_a_missing_bucket_is_no_such_bucket(s3: Any) -> None:
    with raises("NoSuchBucket"):
        s3.get_bucket_policy(Bucket=uniq("conf-nobucket"))


def test_bucket_policy_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    policy = json.dumps(
        {
            "Version": "2012-10-17",
            "Statement": [
                {"Effect": "Deny", "Principal": "*", "Action": "s3:*", "Resource": f"arn:aws:s3:::{bucket}/*",
                 "Condition": {"Bool": {"aws:SecureTransport": "false"}}}
            ],
        }
    )
    assert s3.put_bucket_policy(Bucket=bucket, Policy=policy)["ResponseMetadata"]["HTTPStatusCode"] == 204
    assert json.loads(s3.get_bucket_policy(Bucket=bucket)["Policy"]) == json.loads(policy)
    assert s3.delete_bucket_policy(Bucket=bucket)["ResponseMetadata"]["HTTPStatusCode"] == 204
    with raises("NoSuchBucketPolicy"):
        s3.get_bucket_policy(Bucket=bucket)


def test_bucket_policy_must_be_valid_json(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("MalformedPolicy") as exc:
        s3.put_bucket_policy(Bucket=bucket, Policy="not json")
    assert exc.value.response["Error"]["Message"] == "Policies must be valid JSON and the first byte must be '{'"


def test_bucket_tagging_round_trip_and_validation(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_bucket_tagging(Bucket=bucket, Tagging={"TagSet": [{"Key": "env", "Value": "test"}, {"Key": "a", "Value": "b"}]})
    tags = {t["Key"]: t["Value"] for t in s3.get_bucket_tagging(Bucket=bucket)["TagSet"]}
    assert tags == {"env": "test", "a": "b"}

    with raises("InvalidTag") as exc:
        s3.put_bucket_tagging(Bucket=bucket, Tagging={"TagSet": [{"Key": "k", "Value": "1"}, {"Key": "k", "Value": "2"}]})
    assert exc.value.response["Error"]["Message"] == "Cannot provide multiple Tags with the same key"

    s3.delete_bucket_tagging(Bucket=bucket)
    with raises("NoSuchTagSet"):
        s3.get_bucket_tagging(Bucket=bucket)


def test_bucket_cors_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    rules = [
        {
            "AllowedMethods": ["GET", "PUT"],
            "AllowedOrigins": ["https://example.test"],
            "AllowedHeaders": ["*"],
            "ExposeHeaders": ["ETag"],
            "MaxAgeSeconds": 3000,
        }
    ]
    s3.put_bucket_cors(Bucket=bucket, CORSConfiguration={"CORSRules": rules})
    assert s3.get_bucket_cors(Bucket=bucket)["CORSRules"] == rules
    s3.delete_bucket_cors(Bucket=bucket)
    with raises("NoSuchCORSConfiguration"):
        s3.get_bucket_cors(Bucket=bucket)


def test_bucket_lifecycle_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    rules = [
        {
            "ID": "expire-logs",
            "Status": "Enabled",
            "Filter": {"Prefix": "logs/"},
            "Expiration": {"Days": 30},
            "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 7},
        }
    ]
    s3.put_bucket_lifecycle_configuration(Bucket=bucket, LifecycleConfiguration={"Rules": rules})
    assert s3.get_bucket_lifecycle_configuration(Bucket=bucket)["Rules"] == rules
    s3.delete_bucket_lifecycle(Bucket=bucket)
    with raises("NoSuchLifecycleConfiguration"):
        s3.get_bucket_lifecycle_configuration(Bucket=bucket)


def test_bucket_website_round_trip(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    config = {"IndexDocument": {"Suffix": "index.html"}, "ErrorDocument": {"Key": "error.html"}}
    s3.put_bucket_website(Bucket=bucket, WebsiteConfiguration=config)
    got = s3.get_bucket_website(Bucket=bucket)
    assert got["IndexDocument"] == config["IndexDocument"] and got["ErrorDocument"] == config["ErrorDocument"]
    s3.delete_bucket_website(Bucket=bucket)
    s3.delete_bucket_website(Bucket=bucket)


def test_bucket_encryption_round_trip(s3: Any, make_bucket: Callable[..., str], make_key: Callable[..., dict[str, Any]]) -> None:
    bucket = make_bucket()
    key = make_key()
    rule = {"ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "aws:kms", "KMSMasterKeyID": key["KeyId"]}, "BucketKeyEnabled": True}
    s3.put_bucket_encryption(Bucket=bucket, ServerSideEncryptionConfiguration={"Rules": [rule]})
    got = s3.get_bucket_encryption(Bucket=bucket)["ServerSideEncryptionConfiguration"]["Rules"]
    assert got == [rule]


def test_versioning_keeps_versions_and_delete_markers(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_bucket_versioning(Bucket=bucket, VersioningConfiguration={"Status": "Enabled"})
    assert s3.get_bucket_versioning(Bucket=bucket)["Status"] == "Enabled"

    v1 = s3.put_object(Bucket=bucket, Key="k", Body=b"one")["VersionId"]
    v2 = s3.put_object(Bucket=bucket, Key="k", Body=b"two")["VersionId"]
    assert v1 != v2
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == b"two"
    assert s3.get_object(Bucket=bucket, Key="k", VersionId=v1)["Body"].read() == b"one"

    deleted = s3.delete_object(Bucket=bucket, Key="k")
    assert deleted["DeleteMarker"] is True
    marker = deleted["VersionId"]
    with raises("NoSuchKey") as exc:
        s3.get_object(Bucket=bucket, Key="k")
    assert exc.value.response["ResponseMetadata"]["HTTPHeaders"]["x-amz-delete-marker"] == "true"
    assert "Contents" not in s3.list_objects_v2(Bucket=bucket)

    versions = s3.list_object_versions(Bucket=bucket)
    assert sorted(v["VersionId"] for v in versions["Versions"]) == sorted([v1, v2])
    assert [m["VersionId"] for m in versions["DeleteMarkers"]] == [marker]
    assert [v["VersionId"] for v in versions["Versions"] if v["IsLatest"]] == []
    assert versions["DeleteMarkers"][0]["IsLatest"] is True

    s3.delete_object(Bucket=bucket, Key="k", VersionId=marker)
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == b"two"


def test_unversioned_bucket_rejects_a_real_version_id(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x")
    with raises("InvalidArgument") as exc:
        s3.get_object(Bucket=bucket, Key="k", VersionId="abc")
    assert exc.value.response["Error"]["Message"] == "Invalid version id specified"
    assert s3.get_object(Bucket=bucket, Key="k", VersionId="null")["Body"].read() == b"x"


def test_put_object_with_sse_kms_reports_the_full_key_arn(
    s3: Any, make_bucket: Callable[..., str], make_key: Callable[..., dict[str, Any]]
) -> None:
    bucket = make_bucket()
    alias = f"alias/{uniq('conf-s3')}"
    key = make_key(alias=alias)
    put = s3.put_object(Bucket=bucket, Key="k", Body=b"secret", ServerSideEncryption="aws:kms", SSEKMSKeyId=alias)
    assert put["ServerSideEncryption"] == "aws:kms"
    assert put["SSEKMSKeyId"] == key["Arn"]
    assert put["ETag"] != f'"{md5_hex(b"secret")}"', "an SSE-KMS ETag is not the MD5 of the data"

    got = s3.get_object(Bucket=bucket, Key="k")
    assert got["Body"].read() == b"secret"
    assert got["ServerSideEncryption"] == "aws:kms"
    assert got["SSEKMSKeyId"] == key["Arn"]


def test_bucket_default_encryption_applies_to_new_objects(
    s3: Any, make_bucket: Callable[..., str], make_key: Callable[..., dict[str, Any]]
) -> None:
    bucket = make_bucket()
    key = make_key()
    before = s3.put_object(Bucket=bucket, Key="before", Body=b"x")
    s3.put_bucket_encryption(
        Bucket=bucket,
        ServerSideEncryptionConfiguration={
            "Rules": [{"ApplyServerSideEncryptionByDefault": {"SSEAlgorithm": "aws:kms", "KMSMasterKeyID": key["Arn"]}}]
        },
    )
    after = s3.put_object(Bucket=bucket, Key="after", Body=b"x")
    assert after["ServerSideEncryption"] == "aws:kms" and after["SSEKMSKeyId"] == key["Arn"]
    # Existing objects keep the encryption they were written with.
    assert s3.head_object(Bucket=bucket, Key="before")["ServerSideEncryption"] == before["ServerSideEncryption"] == "AES256"


def test_multipart_upload_with_sse_kms(
    s3: Any, make_bucket: Callable[..., str], make_key: Callable[..., dict[str, Any]]
) -> None:
    bucket = make_bucket()
    key = make_key()
    data = payload(6 * MiB, seed=9)
    s3.upload_fileobj(
        io.BytesIO(data),
        bucket,
        "mp",
        Config=TransferConfig(multipart_threshold=5 * MiB, multipart_chunksize=5 * MiB),
        ExtraArgs={"ServerSideEncryption": "aws:kms", "SSEKMSKeyId": key["KeyId"]},
    )
    head = s3.head_object(Bucket=bucket, Key="mp")
    assert head["ServerSideEncryption"] == "aws:kms" and head["SSEKMSKeyId"] == key["Arn"]
    assert head["ETag"].endswith('-2"')
    assert s3.get_object(Bucket=bucket, Key="mp")["Body"].read() == data


def test_sse_kms_key_validation(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("KMS.NotFoundException") as exc:
        s3.put_object(Bucket=bucket, Key="k", Body=b"x", ServerSideEncryption="aws:kms", SSEKMSKeyId="fake-key-id")
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == "Invalid keyId 'fake-key-id'"

    missing = "arn:aws:kms:us-east-1:000000000000:key/00000000-0000-4000-8000-000000000000"
    with raises("KMS.NotFoundException") as exc:
        s3.put_object(Bucket=bucket, Key="k", Body=b"x", ServerSideEncryption="aws:kms", SSEKMSKeyId=missing)
    assert exc.value.response["Error"]["Message"] == f"Key '{missing}' does not exist"


def test_disabled_key_blocks_writes_and_reads_of_kms_objects(
    s3: Any, kms: Any, make_bucket: Callable[..., str], make_key: Callable[..., dict[str, Any]]
) -> None:
    bucket = make_bucket()
    key = make_key()
    s3.put_object(Bucket=bucket, Key="k", Body=b"x", ServerSideEncryption="aws:kms", SSEKMSKeyId=key["KeyId"])
    kms.disable_key(KeyId=key["KeyId"])
    with raises("KMS.DisabledException") as exc:
        s3.put_object(Bucket=bucket, Key="new", Body=b"x", ServerSideEncryption="aws:kms", SSEKMSKeyId=key["KeyId"])
    assert exc.value.response["Error"]["Message"] == f"{key['Arn']} is disabled."
    with raises("KMS.DisabledException"):
        s3.get_object(Bucket=bucket, Key="k")
    kms.enable_key(KeyId=key["KeyId"])
    assert s3.get_object(Bucket=bucket, Key="k")["Body"].read() == b"x"


def allow_s3_to_send(sqs: Any, queue_url: str, queue_arn: str, bucket: str) -> None:
    policy = {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"Service": "s3.amazonaws.com"},
                "Action": "sqs:SendMessage",
                "Resource": queue_arn,
                "Condition": {"ArnLike": {"aws:SourceArn": f"arn:aws:s3:::{bucket}"}},
            }
        ],
    }
    sqs.set_queue_attributes(QueueUrl=queue_url, Attributes={"Policy": json.dumps(policy)})


def drain(sqs: Any, url: str, count: int, *, timeout: float = 15.0) -> list[dict[str, Any]]:
    seen: list[dict[str, Any]] = []

    def step() -> list[dict[str, Any]]:
        for m in sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, WaitTimeSeconds=1).get("Messages", []):
            seen.append(json.loads(m["Body"]))
            sqs.delete_message(QueueUrl=url, ReceiptHandle=m["ReceiptHandle"])
        return seen

    return eventually(step, lambda s: len(s) >= count, timeout=timeout, interval=0.0, what=f"{count} notifications")


def test_object_events_reach_an_sqs_queue(
    s3: Any,
    sqs: Any,
    admin: Admin,
    make_bucket: Callable[..., str],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
) -> None:
    bucket = make_bucket()
    queue = make_queue("conf-s3events")
    arn = queue_arn(queue)
    allow_s3_to_send(sqs, queue, arn, bucket)
    s3.put_bucket_notification_configuration(
        Bucket=bucket,
        NotificationConfiguration={
            "QueueConfigurations": [
                {
                    "Id": "uploads",
                    "QueueArn": arn,
                    "Events": ["s3:ObjectCreated:*", "s3:ObjectRemoved:*"],
                    "Filter": {"Key": {"FilterRules": [{"Name": "prefix", "Value": "in/"}]}},
                }
            ]
        },
    )
    got = s3.get_bucket_notification_configuration(Bucket=bucket)["QueueConfigurations"]
    assert got[0]["Id"] == "uploads" and got[0]["QueueArn"] == arn
    assert got[0]["Filter"]["Key"]["FilterRules"] == [{"Name": "Prefix", "Value": "in/"}]

    # S3 first sends a one-off test event to the destination.
    [test_event] = drain(sqs, queue, 1)
    assert test_event["Event"] == "s3:TestEvent" and test_event["Bucket"] == bucket

    body = b"event payload"
    s3.put_object(Bucket=bucket, Key="in/a b@c.txt", Body=body)
    s3.put_object(Bucket=bucket, Key="out/ignored.txt", Body=body)
    admin.settle()
    [created] = drain(sqs, queue, 1)
    record = created["Records"][0]
    assert record["eventSource"] == "aws:s3"
    assert record["eventName"] == "ObjectCreated:Put"
    assert record["awsRegion"] == REGION
    assert record["s3"]["bucket"]["name"] == bucket
    assert record["s3"]["bucket"]["arn"] == f"arn:aws:s3:::{bucket}"
    assert record["s3"]["configurationId"] == "uploads"
    assert record["s3"]["object"]["key"] == "in/a+b%40c.txt", "keys are URL-encoded in event records"
    assert record["s3"]["object"]["size"] == len(body)
    assert record["s3"]["object"]["eTag"] == md5_hex(body), "event ETags carry no quotes"

    s3.delete_object(Bucket=bucket, Key="in/a b@c.txt")
    admin.settle()
    [removed] = drain(sqs, queue, 1)
    assert removed["Records"][0]["eventName"] == "ObjectRemoved:Delete"
    assert "size" not in removed["Records"][0]["s3"]["object"]

    assert sqs.receive_message(QueueUrl=queue).get("Messages") is None, "the out/ key must not notify"


def test_multipart_completion_notifies_as_complete_multipart_upload(
    s3: Any,
    sqs: Any,
    admin: Admin,
    make_bucket: Callable[..., str],
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
) -> None:
    bucket = make_bucket()
    queue = make_queue("conf-s3mp")
    arn = queue_arn(queue)
    allow_s3_to_send(sqs, queue, arn, bucket)
    s3.put_bucket_notification_configuration(
        Bucket=bucket,
        NotificationConfiguration={"QueueConfigurations": [{"QueueArn": arn, "Events": ["s3:ObjectCreated:*"]}]},
    )
    drain(sqs, queue, 1)  # the test event
    s3.upload_fileobj(
        io.BytesIO(payload(6 * MiB, seed=13)),
        bucket,
        "mp",
        Config=TransferConfig(multipart_threshold=5 * MiB, multipart_chunksize=5 * MiB),
    )
    admin.settle()
    [event] = drain(sqs, queue, 1)
    assert event["Records"][0]["eventName"] == "ObjectCreated:CompleteMultipartUpload"
    assert event["Records"][0]["s3"]["object"]["size"] == 6 * MiB


def test_notification_to_a_missing_queue_is_rejected(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    arn = f"arn:aws:sqs:us-east-1:000000000000:{uniq('conf-missing')}"
    with raises("InvalidArgument") as exc:
        s3.put_bucket_notification_configuration(
            Bucket=bucket,
            NotificationConfiguration={"QueueConfigurations": [{"QueueArn": arn, "Events": ["s3:ObjectCreated:*"]}]},
        )
    assert exc.value.response["Error"]["Message"] == "Unable to validate the following destination configurations"


def test_notification_with_a_malformed_arn_is_rejected(s3: Any, make_bucket: Callable[..., str]) -> None:
    bucket = make_bucket()
    with raises("InvalidArgument") as exc:
        s3.put_bucket_notification_configuration(
            Bucket=bucket,
            NotificationConfiguration={"QueueConfigurations": [{"QueueArn": "not-an-arn", "Events": ["s3:ObjectCreated:*"]}]},
        )
    assert exc.value.response["Error"]["Message"] == "The ARN could not be parsed"
    assert exc.value.response["Error"]["ArgumentName"] == "Queue"

