# ESMS-01: validation texts for stream parameters (lambda-stream-sources.md 13.2)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)

| Field | Result |
|---|---|
| every step | n/a (free-plan SCP: Kinesis is denied in this account; every step creates against the Kinesis stream `$KS` (steps 15-16 need it to exist; step 14 is another Region)). Not run |
