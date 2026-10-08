# Gap G4: Lambda in-container runtime contract

Full findings: [lambda-runtime-contract.md](lambda-runtime-contract.md).

**Closed.**

- Clock domains: containers, SDK signers and the RICs all read the host wall clock, and Linux time namespaces do not virtualise `CLOCK_REALTIME` ([time_namespaces(7)](https://man7.org/linux/man-pages/man7/time_namespaces.7.html)). `Lambda-Runtime-Deadline-Ms` is `dispatch_wall_ms + Timeout`, function timeout and REPORT durations use a monotonic timer, and the emulator clock governs only emulator-observed timers (async retry delays, ESM windows, `Last-Modified`, TTL sweep, presign expiry, retention). Observed with Python 3.13 and `provided.al2023` Go runtimes against a stub Runtime API: a -1 h or frozen deadline gives aws-lambda-go an already-cancelled context (SDK calls fail before sending) and Python a remaining time of 0.
- Reachability: only JS v3 routes SQS calls by `QueueUrl` (even over `AWS_ENDPOINT_URL`); Go v2, boto3 and the CLI use the configured endpoint. Go and JS use virtual-hosted S3 for any non-IP custom endpoint. Recommended QueueUrl strategy: echo the request Host, inject an IPv4-literal endpoint into functions, and add `localhost:host-gateway` plus a gateway listener on Linux/Podman (observed to work for Go, boto3, Node and urllib, not curl 8.21).

**Corrections made in place** (each marked `G4 correction` or `G4 verification`): `lambda-api.md` 12 items 1 and 16, `secrets-logs-events-alarms.md` 3.3 and 7.12, `s3.md` presign bullet and item 14, `sigv4-checksums-endpoints.md` Implications 3, 10, 12, `iam-sts-kms.md` item 8, `containers.md` 8.5 and 14.4 Q3.

**Still UNVERIFIED:** `localhost:host-gateway` on Docker Desktop, rootless Docker and Podman; blackhole connect timings per SDK; which timestamp CloudWatch Logs retention uses; the EventBridge envelope `time` description; Node and Java RIC behavior beyond source reading.
