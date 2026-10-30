"""SES v2: email identities, their tags and GetAccount (ses.md sections 2.3, 5.4, 6.3 and 9.3).

This client (botocore 1.43.111) sends ListEmailIdentities as POST
/v2/email/list-identities (ses.md 3.2).
"""

from __future__ import annotations

from typing import Any

from support import REGION, raises, status_of, uniq


def test_domain_identity_lifecycle(sesv2: Any, cleanup: Any) -> None:
    domain = uniq("conf-sesv2-domain") + ".example.org"
    created = sesv2.create_email_identity(EmailIdentity=domain, Tags=[{"Key": "env", "Value": "dev"}])
    cleanup.add(sesv2.delete_email_identity, EmailIdentity=domain)
    assert created["IdentityType"] == "DOMAIN"
    assert created["VerifiedForSendingStatus"] is False
    # The ses.md 5.4 domain column: PENDING, three stable tokens, RSA_2048_BIT
    # both, a key generation time.
    got = sesv2.get_email_identity(EmailIdentity=domain)
    dkim = got["DkimAttributes"]
    assert got["VerificationStatus"] == "PENDING"
    assert dkim["Status"] == "PENDING"
    assert len(dkim["Tokens"]) == 3
    assert dkim["NextSigningKeyLength"] == "RSA_2048_BIT"
    assert dkim["CurrentSigningKeyLength"] == "RSA_2048_BIT"
    assert "LastKeyGenerationTimestamp" in dkim
    assert sesv2.get_email_identity(EmailIdentity=domain)["DkimAttributes"]["Tokens"] == dkim["Tokens"]
    # Listed regardless of status (ses.md 5.4).
    names = [i["IdentityName"] for i in sesv2.list_email_identities(PageSize=1000)["EmailIdentities"]]
    assert domain in names
    # Tags through the generic tag operations on the identity ARN (ses.md 5.4, 9.3).
    arn = f"arn:aws:ses:{REGION}:000000000000:identity/{domain}"
    sesv2.tag_resource(ResourceArn=arn, Tags=[{"Key": "team", "Value": "mail"}])
    sesv2.untag_resource(ResourceArn=arn, TagKeys=["env"])
    assert sesv2.list_tags_for_resource(ResourceArn=arn)["Tags"] == [{"Key": "team", "Value": "mail"}]


def test_deleted_identity_is_not_found(sesv2: Any) -> None:
    domain = uniq("conf-sesv2-gone") + ".example.org"
    sesv2.create_email_identity(EmailIdentity=domain)
    sesv2.delete_email_identity(EmailIdentity=domain)
    # NotFoundException, 404 per the model (ses.md 3.3), which Terraform
    # reads as gone (ses.md 9.3).
    with raises("NotFoundException") as info:
        sesv2.get_email_identity(EmailIdentity=domain)
    assert status_of(info.value) == 404


def test_address_is_shared_with_v1(sesv2: Any, ses: Any, cleanup: Any) -> None:
    addr = uniq("conf-sesv2-address") + "@example.com"
    sesv2.create_email_identity(EmailIdentity=addr)
    cleanup.add(sesv2.delete_email_identity, EmailIdentity=addr)
    # The ses.md 5.4 email column: a DKIM block with NOT_STARTED and no tokens.
    dkim = sesv2.get_email_identity(EmailIdentity=addr)["DkimAttributes"]
    assert dkim["Status"] == "NOT_STARTED"
    assert dkim.get("Tokens", []) == []
    assert dkim["NextSigningKeyLength"] == "RSA_1024_BIT"
    # One store behind both APIs (ses.md 2.3, switch ses.store.shared-v1-v2).
    attrs = ses.get_identity_verification_attributes(Identities=[addr])["VerificationAttributes"]
    assert attrs[addr]["VerificationStatus"] == "Pending"


def test_get_account_reports_the_sandbox(sesv2: Any) -> None:
    # ProductionAccessEnabled false is the sandbox (model, ses.md 6.3), with
    # the recorded sandbox quota of 200 and 1 (ses.md 6.3).
    account = sesv2.get_account()
    assert account["ProductionAccessEnabled"] is False
    assert account["SendQuota"]["Max24HourSend"] == 200
    assert account["SendQuota"]["MaxSendRate"] == 1
