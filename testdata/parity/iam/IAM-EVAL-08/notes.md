# IAM-EVAL-08

Date: 2026-11-03 (03:11-03:15Z)
Region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
Caller: assumed role (not root), so the 13.1 IAM-user switch was not run.
Label: aws-recorded, simulator (simulate-custom-policy). Recipe fixes: none. The doc states no Expected; recorded:

- bool-titlecase (`True`) allowed; bool-uppercase (`TRUE`) allowed; bool-1 implicitDeny.
- The steps were recorded as bool-True and bool-TRUE and renamed on 2026-11-06: the two names collide on a case-insensitive filesystem.
- ignorecase-nonascii (ÄRGER vs ärger) allowed: non-ASCII case folding applies.
