# SFN-14 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-api.md 14.15
- account ids masked as <acct> below

## Expected vs AWS

- archive: https://s3.amazonaws.com/stepfunctionslocal/StepFunctionsLocal.zip, sha256 386837d4a6a4973557f403cf6eca7eb4584979a8f033d2b1b656a6833a955916 (sha256.txt), entries dated 05-18-2024 (matches 12.1 build 2024-05-18); contains StepFunctionsLocal.jar, LICENSE.txt (716 bytes), NOTICE.txt (93 bytes, not extracted: the recipe's patterns are LICENSE* and THIRD-PARTY*), third_party_licenses/ (no THIRD-PARTY* file, so that pattern matched nothing)
- licence (UNVERIFIED 12.2): matches the Docker Hub text -- LICENSE.txt: `AWS Step Functions Local is licensed pursuant to the AWS Customer Agreement (currently found here: https://aws.amazon.com/agreement/) or other agreement between you and us governing your use of our Service Offerings. Some components ... may also be governed by applicable open source software licenses ...` (licence.txt). 12.5's recommendation stands.
- the JAR was not run
- session: the recipe teardown `rm -rf "$ZD"` ran in the script
