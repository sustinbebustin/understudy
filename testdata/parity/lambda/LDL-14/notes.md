date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:). Step 04 rerun by ldl-rerun.sh: the AWS CLI refuses `--zip-file` that is not a zip before sending (`--zip-file must be a zip file with the fileb:// prefix.`), so the same 9 bytes went as `Content.ZipFile` through `--cli-input-json` (# session:). Note: 03-publish-53-zipped.http is 70 MB (the debug log holds the request body).

- 01 `Function code combined with layers exceeds the maximum allowed size ...`: differs: 400 `InvalidParameterValueException`, body `{"Type":"User","message":"Layers consume more than the available size of 262144000 bytes"}` (two 140 MiB layers, 7.1).
- 02 260 MiB unzipped: 400 `InvalidParameterValueException`, `{"Type":"User","message":"Unzipped size must be smaller than 262144000 bytes"}`.
- 03 53 MB zipped: 413 `RequestEntityTooLargeException`, `{"Message":"Request must be smaller than 70167211 bytes for the PublishLayerVersion operation"}` (capital `Message`, no `Type`). The limit is on the request (base64 body), not the 50 MB archive.
- 04 not a zip: 400 `InvalidParameterValueException`, `{"Type":"User","message":"Could not unzip uploaded file. Please check your file, then try to upload again."}`.
