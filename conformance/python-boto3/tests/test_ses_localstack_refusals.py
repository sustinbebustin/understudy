"""LocalStack's SES tests that need what understudy leaves unbound: each runs
the test's calls up to the first one understudy refuses and asserts that call
answers 501 NotImplemented naming its operation, as every unbound operation
does (ses.md 11.1 "Unbound"; configuration sets and receipt rules are the
deferred second slice).

Each cites localstack@8b9a79f tests/aws/services/ses/test_ses.py with its
line range and its last validation date from test_ses.validation.json.
"""

from __future__ import annotations

import json
from typing import Any, Callable

from support import raises, status_of, uniq


def _refused(err: Any, operation: str) -> None:
    assert status_of(err.value) == 501
    assert f"understudy does not implement ses.{operation}." in err.value.response["Error"]["Message"]


def test_deleting_non_existent_configuration_set(ses: Any) -> None:
    # test_deleting_non_existent_configuration_set, L830-L837, validated
    # 2023-08-25: the first call, DeleteConfigurationSet (L836), is refused.
    with raises("NotImplemented") as err:
        ses.delete_configuration_set(ConfigurationSetName=uniq("config-set"))
    _refused(err, "DeleteConfigurationSet")


def test_creating_event_destination_without_configuration_set(ses: Any, sns: Any, cleanup: Any) -> None:
    # test_creating_event_destination_without_configuration_set, L807-L828,
    # validated 2023-08-25: the sns_topic fixture succeeds, then
    # CreateConfigurationSetEventDestination (L817) is refused.
    topic_arn = sns.create_topic(Name=uniq("conf-ses-topic"))["TopicArn"]
    cleanup.add(sns.delete_topic, TopicArn=topic_arn)
    with raises("NotImplemented") as err:
        ses.create_configuration_set_event_destination(
            ConfigurationSetName=uniq("nonexistent-configuration-set"),
            EventDestination={
                "Name": uniq("event-destination"),
                "Enabled": True,
                "MatchingEventTypes": ["send", "bounce", "delivery", "open", "click"],
                "SNSDestination": {"TopicARN": topic_arn},
            },
        )
    _refused(err, "CreateConfigurationSetEventDestination")


def test_ses_sns_topic_integration_send_email(
    ses: Any,
    sns: Any,
    sqs: Any,
    make_queue: Callable[..., str],
    queue_arn: Callable[[str], str],
    cleanup: Any,
) -> None:
    # test_ses_sns_topic_integration_send_email, L496-L570, validated
    # 2023-08-25 and marked manual_setup_required: the SQS and SNS fixtures,
    # setup_email_addresses() (L533, two VerifyEmailIdentity calls) and the
    # SQS subscription (L542) succeed; CreateConfigurationSet from the
    # ses_configuration_set fixture (L545) is refused.
    queue_url = make_queue("conf-ses-events")
    topic_arn = sns.create_topic(Name=uniq("conf-ses-topic"))["TopicArn"]
    cleanup.add(sns.delete_topic, TopicArn=topic_arn)
    for address in (uniq("sender") + "@example.com", uniq("recipient") + "@example.com"):
        ses.verify_email_identity(EmailAddress=address)
        cleanup.add(ses.delete_identity, Identity=address)
    arn = queue_arn(queue_url)
    subscription = sns.subscribe(TopicArn=topic_arn, Protocol="sqs", Endpoint=arn)["SubscriptionArn"]
    sqs.set_queue_attributes(
        QueueUrl=queue_url,
        Attributes={
            "Policy": json.dumps(
                {
                    "Statement": [
                        {
                            "Effect": "Allow",
                            "Principal": {"Service": "sns.amazonaws.com"},
                            "Action": "sqs:SendMessage",
                            "Resource": arn,
                            "Condition": {"ArnEquals": {"aws:SourceArn": topic_arn}},
                        }
                    ]
                }
            )
        },
    )
    sns.get_subscription_attributes(SubscriptionArn=subscription)
    with raises("NotImplemented") as err:
        ses.create_configuration_set(ConfigurationSet={"Name": uniq("config-set")})
    _refused(err, "CreateConfigurationSet")


def test_clone_receipt_rule_set(ses: Any, make_bucket: Callable[..., str]) -> None:
    # test_clone_receipt_rule_set, L324-L425, validated 2025-09-17: the
    # s3_bucket fixture succeeds, then CreateReceiptRuleSet from the
    # ses_create_receipt_rule_set fixture (L350) is refused.
    make_bucket("conf-ses-rules")
    with raises("NotImplemented") as err:
        ses.create_receipt_rule_set(RuleSetName="RuleSetToClone")
    _refused(err, "CreateReceiptRuleSet")
