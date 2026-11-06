date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: run by ldl-rerun.sh. ldl.sh's first run could not create the function ($LX empty, # session:); captures removed and rerun against $P-ldl-x version 2. $LX was not refused in LDL-13, so it was used as the recipe says.

- 01 no `Layers` key or `[]`: on the wire `"Layers":null` (key present, null) in the update response and in the get; the CLI drops the null. 7.2's "no Layers key" comes from a snapshot that drops nulls.
- 02 the DLQ still set: matches finding 2: `"DeadLetterConfig":{"TargetArn":"arn:aws:sqs:us-east-2:<A>:us1103-ldl-dlq"}` after `{}` (understudy clears it: differs from understudy, REPO config.go:L148-L150).
- 03 no `DeadLetterConfig` or no `TargetArn`: `"DeadLetterConfig":null` in the update response and the get. Matches (cleared).
