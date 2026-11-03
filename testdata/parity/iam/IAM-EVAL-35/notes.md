# IAM-EVAL-35

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Recipe fixes: none.

- 01-role-template: matches (branch: created; policydoc's IAM resource path list gains role-template/). Policy deleted.
- 02-assumed-role: matches (branch: refused) -- MalformedPolicyDocument: IAM resource path must either be "*", root, or start with user/, federated-user/, role/, group/, instance-profile/, mfa/, server-certificate/, policy/, sms-mfa/, saml-provider/, oidc-provider/, report/, access-report/, role-template/.
- 03-delegation-request: matches (branch: refused) -- same message, same list verbatim.
- 04-report: matches (branch: created; report/ stays). Policy deleted.
- Note: AWS's list also includes root, sms-mfa/, access-report/ and role-template/; record against policydoc's list.
