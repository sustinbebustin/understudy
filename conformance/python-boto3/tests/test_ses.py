"""SES v1: email identities (ses.md sections 5 and 9.1)."""

from __future__ import annotations

from typing import Any

from support import uniq


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
