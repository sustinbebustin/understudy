"""SES v1: email and domain identities (ses.md sections 5, 9.1 and 9.2)."""

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
