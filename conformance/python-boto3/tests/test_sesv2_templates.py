"""SES v2 email templates and the deprecated v1 email-address operations (ses.md sections 2.3, 3.2, 5.3 and 7)."""

from __future__ import annotations

from typing import Any

from support import REGION, raises, status_of, uniq


def test_email_template_lifecycle(sesv2: Any, ses: Any, cleanup: Any) -> None:
    name = uniq("conf-sesv2-template")
    sesv2.create_email_template(
        TemplateName=name,
        TemplateContent={"Subject": "Hi {{name}}", "Text": "Dear {{name}}"},
        Tags=[{"Key": "env", "Value": "dev"}],
    )
    cleanup.add(ses.delete_template, TemplateName=name)
    got = sesv2.get_email_template(TemplateName=name)
    assert got["TemplateContent"] == {"Subject": "Hi {{name}}", "Text": "Dear {{name}}"}
    assert got["Tags"] == [{"Key": "env", "Value": "dev"}]
    # One store for v1 and v2 templates (ses.md 2.3).
    sesv2.update_email_template(TemplateName=name, TemplateContent={"Subject": "Hello {{name}}"})
    v1 = ses.get_template(TemplateName=name)["Template"]
    assert (v1.get("SubjectPart"), v1.get("TextPart")) == ("Hello {{name}}", None)
    names = [m["TemplateName"] for m in sesv2.list_email_templates(PageSize=100)["TemplatesMetadata"]]
    assert name in names
    # Tags through the generic tag operations on the template ARN (ses.md 3.2).
    arn = f"arn:aws:ses:{REGION}:000000000000:template/{name}"
    sesv2.tag_resource(ResourceArn=arn, Tags=[{"Key": "team", "Value": "mail"}])
    sesv2.untag_resource(ResourceArn=arn, TagKeys=["env"])
    assert sesv2.list_tags_for_resource(ResourceArn=arn)["Tags"] == [{"Key": "team", "Value": "mail"}]
    sesv2.delete_email_template(TemplateName=name)
    # NotFoundException, 404 per the model (ses.md 3.3, 7).
    with raises("NotFoundException") as info:
        sesv2.get_email_template(TemplateName=name)
    assert status_of(info.value) == 404


def test_legacy_email_address_operations(ses: Any, cleanup: Any) -> None:
    addr = uniq("conf-ses-legacy") + "@example.com"
    # VerifyEmailAddress is the deprecated VerifyEmailIdentity (ses.md 5.3).
    ses.verify_email_address(EmailAddress=addr)
    cleanup.add(ses.delete_identity, Identity=addr)
    attrs = ses.get_identity_verification_attributes(Identities=[addr])["VerificationAttributes"]
    assert attrs[addr]["VerificationStatus"] == "Pending"
    assert addr not in ses.list_verified_email_addresses()["VerifiedEmailAddresses"]
    ses.delete_verified_email_address(EmailAddress=addr)
    assert addr not in ses.get_identity_verification_attributes(Identities=[addr])["VerificationAttributes"]
