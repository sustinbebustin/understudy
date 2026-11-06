date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:). The account held no layers before 01 (checked with list-layers before the run).

- 01 `{"Layers": []}` or `{}` in the raw body: differs from both: `{"NextMarker":null,"Layers":[]}` (200, 31 bytes; `NextMarker` present as null).
- 02 whether `$P-ldl-del` appears: it does not; a layer whose only version was deleted is not listed. Only `us1103-ldl-x` is listed.
- 03 `NextMarker` present when two layers exist: n/a (only one layer is listed, since $P-ldl-del is not): 03 (MaxItems=1) and 04 return the same body with `"NextMarker":null`.
- default page size: n/a (one layer; not determinable).
- item shape (6.5): `{"LatestMatchingVersion":{"CompatibleRuntimes":["nodejs22.x"],"LicenseInfo":null,"Description":null,"LayerVersionArn":"...:layer:us1103-ldl-x:1","Version":1,"CreatedDate":"2026-11-03T17:06:24.940+0000","CompatibleArchitectures":["arm64"]},"Description":null,"LayerName":"us1103-ldl-x","State":null,"LayerArn":"...:layer:us1103-ldl-x"}`. Not in 6.5: a top-level `Description` and `State` on the list item (both null), and `Description` null rather than `""` (6.3 says `""` in the publish response). The wire carries nulls that the CLI hides.
