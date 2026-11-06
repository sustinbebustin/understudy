# IAM-EVAL-12

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none. The doc states no Expected; recorded:

- 2012-10-17: star (a*b) allowed, x (axb) implicitDeny, literal (a${*}b) implicitDeny => ${*} is a literal *.
- 2008-10-17: star implicitDeny, x implicitDeny, literal allowed => no variable substitution, ${*} literal text.
- stringequals (a${*}b vs a*b) allowed.
