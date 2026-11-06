date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: run by ldl-rerun.sh. ldl.sh's first run sent nothing ($LX empty: the parent shell's keys had expired, # session:); those captures were removed and the item rerun against a re-published $P-ldl-x version 2 (nodejs22.x, arm64).

- 01 runtime and architecture both differ: succeeds, 201; `Layers` `[{"SigningProfileVersionArn":null,"CodeSize":112,"UncompressedCodeSize":2,"SigningJobArn":null,"Arn":"...:layer:us1103-ldl-x:2"}]`. Matches `lambda.layers.compatibility` = `not-enforced` (7.3).
- 02 runtime only: succeeds, 201. Matches.
- 04 architecture only: succeeds, 201. Matches.
- 03 text: differs from understudy's `Please don't provide Handler or Runtime or Layer when the intended function PackageType is Image.` (7.1): 400 `InvalidParameterValueException`, body `{"Type":null,"message":"Lambda layers are not supported for functions created with container images."}` (`Type` null).
- 03 whether it precedes the image check: yes; the ECR image `<A>.dkr.ecr.us-east-2.amazonaws.com/us1103-nosuch:latest` does not exist, and the layer refusal came instead of an image error.
- Not in 7.2: each `Layers` item also carries `UncompressedCodeSize`.
