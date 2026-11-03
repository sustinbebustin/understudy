# IAM-EVAL-33

Date: 2026-11-03 (first run 03:16-03:25Z; reruns 16:40-16:47Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root); the 13.1 IAM-user switch was not run.
Recipe fixes: none. The doc states no Expected; recorded:

- type-present: delivered (aws:PrincipalType present for the SNS service principal).
- type-absent: nothing received in its own step; its body arrived during star-principal (SNS redelivery once the policy allowed it).
- account-present: delivered (aws:PrincipalAccount present).
- userid-present: delivered (aws:userid present).
- star-principal {"AWS":"*"}: admitted SNS (the late type-absent message arrived under it); its own message was not seen (receive-message returns one message by default), so undetermined for that body.
