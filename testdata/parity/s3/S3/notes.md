# S3-01: x-amz-checksum-mode values other than ENABLED (s3.md 15.1)

- Date: 2026-11-03, 03:10Z
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux x86_64

| Step | Expected | Result |
|---|---|---|
| 01-get-upper, 01-head-upper | 200 with `x-amz-checksum-crc64nvme` and `x-amz-checksum-type: FULL_OBJECT` | matches (`Ffmx7kz9nB0=`, `FULL_OBJECT`) |
| 01-get-lower, 01-get-mixed, 01-head-lower | same two headers, same values | matches |
| 01-get-junk | UNVERIFIED: 400 InvalidArgument or 200 without checksum headers | differs from both guesses: 400 `InvalidRequest`, `<Message>Value for x-amz-checksum-mode header is invalid.</Message>`, no ArgumentName/ArgumentValue |
| 01-head-junk | status alone | 400 (no body) |
| 01-*-raw (recurl) | | deferred: waits for the curl request-body capture fix |
