# Gap G8: S3 xxHash digest encoding and the record-mode backlog

Full write-up: [s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md).

## Result

- XXHASH64, XXHASH3 and XXHASH128 are implementable now. The AWS CRT writes the digest big-endian with seed 0 (8, 8 and 16 bytes; XXH3-128 as `high64` then `low64`); botocore, the Java v2 SDK and the C++ SDK all wrap it and send standard padded base64 in `x-amz-checksum-xxhash64|xxhash3|xxhash128`. Source: [aws-checksums xxhash.c L227-L252](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/xxhash.c#L227-L252), [botocore httpchecksum.py L131-L164](https://github.com/boto/botocore/blob/bfb412faf00c249a54816e287a09a8bb79442c65/botocore/httpchecksum.py#L131-L164).
- Vectors for `""`, `hello`, `123456789`, a 1 MiB pattern and 15 length classes are published and agree with `awscrt` 0.37.0 and Python `xxhash` 4.0.1 (a Go reference written outside the repo reproduces them).
- Trailers: botocore, Java and C++ name the trailer `x-amz-checksum-<algorithm>` for every algorithm they compute; the S3 user guide lists only crc32, crc32c, crc64nvme, sha1 and sha256. Live S3 acceptance of sha512 and xxhash trailer names is UNVERIFIED (backlog item R08). No SDK sends MD5 as a flexible checksum.
- Backlog: 18 ranked record-mode items with request scripts (R01 SQS receipt handles, R02 DLQ and redrive, R03 Lambda SQS mapping, R04 DynamoDB lock-table TTL and PITR shapes, then Terraform read-shape bundles, S3 checksum acceptance, SNS filters, Logs patterns, wire edges, and the rest). Items that need no real AWS are listed separately.

## Corrections made in other files

| File | Change | Source |
|---|---|---|
| `s3.md` 6.1, 9 (item 9), open questions | XXHash digest and seed marked settled; MD5 row corrected (no SDK computes it as a flexible checksum; was "enum only in Go"); SHA512 and XXHASH SDK-support columns corrected (Java and C++ also compute them; Rust does not); the "return unimplemented" advice replaced | CRT, botocore, Java v2, C++, smithy-rs lines in the full write-up |
| `go-toolchain.md` 7.1, 17.7, open questions | UNVERIFIED XXHASH digest text replaced with the settled form; ADR option (a) favoured | same |
| `sigv4-checksums-endpoints.md` 4.2, 9, 12 item 7 | digest form added; trailer-name gap narrowed to live-S3 acceptance | same |
| `smithy-protocol-tests.md` 3, 7, open items | `rpcv2Cbor` is required in phase 1 (Go v2 speaks it to CloudWatch; JS v3 uses awsJson1_0; botocore uses awsJson1_0, CORRECTED (G1): this said CBOR, see [wire-cbor.md](wire-cbor.md)); previously "UNVERIFIED" and "out of phase 1" | [Go api_client.go L215](https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03b3990bfd50208a8a05df1d8c5eac/service/cloudwatch/api_client.go#L215), [JS runtimeConfig.shared.ts L36-L44](https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abed1302aaae350a8bbfa40f60ef32d/clients/client-cloudwatch/src/runtimeConfig.shared.ts#L36-L44) |
| `tf-compute.md` 11 | JS v3 CloudWatch protocol filled in | same |

One naming note: the task text cites `aws/aws-c-checksums`; the repository that holds the xxHash code is `awslabs/aws-checksums`.
