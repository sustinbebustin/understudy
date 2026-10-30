"""SES v1: email and domain identities (ses.md sections 5, 9.1 and 9.2)."""

from __future__ import annotations

from typing import Any

import pytest
from botocore.exceptions import ClientError
from support import error_code, status_of, uniq


def test_verify_email_identity_starts_pending(ses: Any, cleanup: Any) -> None:
    addr = uniq("conf-ses-pending") + "@example.com"
    ses.verify_email_identity(EmailAddress=addr)
    cleanup.add(ses.delete_identity, Identity=addr)
    # Pending until the owner opens the link (ses.md 5.2), keyed by the
    # request string (ses.md 9.1).
    attrs = ses.get_identity_verification_attributes(Identities=[addr])["VerificationAttributes"]
    assert list(attrs) == [addr]
    assert attrs[addr]["VerificationStatus"] == "Pending"


def test_delete_identity_removes_the_key(ses: Any) -> None:
    addr = uniq("conf-ses-delete") + "@example.com"
    ses.verify_email_identity(EmailAddress=addr)
    ses.delete_identity(Identity=addr)
    # Terraform reads a missing key as gone (ses.md 9.1).
    assert ses.get_identity_verification_attributes(Identities=[addr])["VerificationAttributes"] == {}


def test_domain_identity_token_is_stable(ses: Any, cleanup: Any) -> None:
    domain = uniq("conf-ses-domain") + ".example.org"
    token = ses.verify_domain_identity(Domain=domain)["VerificationToken"]
    cleanup.add(ses.delete_identity, Identity=domain)
    # 44 base64 characters, the encoding of 32 bytes (ses.md 5.3), the same
    # on a repeat and on every read (ses.md 9.2).
    assert len(token) == 44
    assert ses.verify_domain_identity(Domain=domain)["VerificationToken"] == token
    attrs = ses.get_identity_verification_attributes(Identities=[domain])["VerificationAttributes"]
    assert attrs[domain] == {"VerificationStatus": "Pending", "VerificationToken": token}
    # Listed regardless of status, under its type only (ses.md 5.3).
    pages = ses.get_paginator("list_identities")
    domains = [i for p in pages.paginate(IdentityType="Domain") for i in p["Identities"]]
    addresses = [i for p in pages.paginate(IdentityType="EmailAddress") for i in p["Identities"]]
    assert domain in domains
    assert domain not in addresses


def test_send_email_rejects_an_invalid_tag_name(ses: Any) -> None:
    # Tag validation runs before the sender check: an unverified Source with
    # an invalid tag name answers the recorded text (third-party-recorded
    # 2024-07-30, ses.md 4.5; localstack@8b9a79f
    # tests/aws/services/ses/test_ses.snapshot.json:L592-L735, validated
    # 2024-07-30).
    with pytest.raises(ClientError) as exc:
        ses.send_email(
            Source=uniq("conf-ses-unverified") + "@example.com",
            Destination={"ToAddresses": ["success@example.com"]},
            Message={"Subject": {"Data": "subject"}, "Body": {"Text": {"Data": "text"}}},
            Tags=[{"Name": "test_invalid_name:123", "Value": "test"}],
        )
    assert error_code(exc.value) == "InvalidParameterValue"
    assert status_of(exc.value) == 400
    assert exc.value.response["Error"]["Message"] == (
        "Invalid tag name <test_invalid_name:123>: only alphanumeric ASCII characters, '_',  '-' are allowed."
    )
