"""SES v1: identities, SendEmail, SendRawEmail and the quota (ses.md sections 4, 5, 6, 9.1 and 9.2)."""

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


def test_send_email_rejects_an_unverified_source(ses: Any) -> None:
    # MessageRejected with the documented text naming the Source (aws-doc and
    # capture, ses.md 6.1).
    source = uniq("conf-ses-unverified") + "@example.com"
    with pytest.raises(ClientError) as exc:
        ses.send_email(
            Source=source,
            Destination={"ToAddresses": ["success@simulator.amazonses.com"]},
            Message={"Subject": {"Data": "subject"}, "Body": {"Text": {"Data": "text"}}},
        )
    assert error_code(exc.value) == "MessageRejected"
    message = exc.value.response["Error"]["Message"]
    assert message.startswith("Email address is not verified. The following identities failed the check in region ")
    assert source in message


def test_get_send_quota(ses: Any) -> None:
    # A sandbox account: 200 a day at 1 a second (golden get-quota-0,
    # third-party-recorded 2024-11-27, ses.md 6.3; localstack@8b9a79f
    # tests/aws/services/ses/test_ses.snapshot.json:L905-L918, validated
    # 2024-11-27).
    quota = ses.get_send_quota()
    assert quota["Max24HourSend"] == 200.0
    assert quota["MaxSendRate"] == 1.0


def test_notification_attributes_key_a_known_identity(ses: Any, cleanup: Any) -> None:
    addr = uniq("conf-ses-notif") + "@example.com"
    ses.verify_email_identity(EmailAddress=addr)
    cleanup.add(ses.delete_identity, Identity=addr)
    # The aws_ses_email_identity data source needs the key (ses.md 9.1).
    attrs = ses.get_identity_notification_attributes(Identities=[addr])["NotificationAttributes"]
    assert list(attrs) == [addr]


def test_send_raw_email_raises_message_rejected(ses: Any) -> None:
    # Golden test_send_email_raises_message_rejected: an unverified raw From
    # and destination answer MessageRejected (third-party-recorded
    # 2025-09-11, ses.md 4.2; localstack@8b9a79f
    # tests/aws/services/ses/test_ses.py:L1162-L1170, validated 2025-09-11).
    raw = "From: origin@example.com\r\nTo: invalid@example.com\r\nSubject: test\r\n\r\nThis is the message body.\r\n"
    with pytest.raises(ClientError) as exc:
        ses.send_raw_email(Destinations=["invalid@example.com"], RawMessage={"Data": raw})
    assert error_code(exc.value) == "MessageRejected"


def _template_names(ses: Any) -> list[str]:
    pages = ses.get_paginator("list_templates").paginate()
    metadata = [m for p in pages for m in p["TemplatesMetadata"]]
    assert all({"Name", "CreatedTimestamp"} <= set(m) for m in metadata)
    return [m["Name"] for m in metadata]


def test_template_lifecycle(ses: Any) -> None:
    # Goldens test_list_templates and test_delete_template: TemplatesMetadata
    # of {CreatedTimestamp, Name}, and the template gone after DeleteTemplate
    # (third-party-recorded 2023-08-25, ses.md 7; localstack@8b9a79f
    # tests/aws/services/ses/test_ses.snapshot.json:L736-L807, validated
    # 2023-08-25).
    name = uniq("conf-ses-template")
    ses.create_template(Template={"TemplateName": name, "SubjectPart": "Hi {{name}}", "TextPart": "Dear {{name}}"})
    assert name in _template_names(ses)
    # GetTemplate echoes the parts given and omits the rest; UpdateTemplate
    # replaces them (ses.md 9.4).
    assert ses.get_template(TemplateName=name)["Template"] == {
        "TemplateName": name,
        "SubjectPart": "Hi {{name}}",
        "TextPart": "Dear {{name}}",
    }
    with pytest.raises(ClientError) as exc:
        ses.create_template(Template={"TemplateName": name})
    assert error_code(exc.value) == "AlreadyExists"
    ses.update_template(Template={"TemplateName": name, "HtmlPart": "<p>new</p>"})
    assert ses.get_template(TemplateName=name)["Template"] == {"TemplateName": name, "HtmlPart": "<p>new</p>"}
    ses.delete_template(TemplateName=name)
    assert name not in _template_names(ses)
    # A missing template is TemplateDoesNotExist (ses.md 7).
    with pytest.raises(ClientError) as exc:
        ses.get_template(TemplateName=name)
    assert error_code(exc.value) == "TemplateDoesNotExist"
