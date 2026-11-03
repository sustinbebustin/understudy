date: 2026-11-03
region: us-east-2
aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
Session: helper files under $REC/_work/LDL (# session:).

- a record arriving in the re-created queue means Lambda retries; none within 10 minutes plus a metric datapoint means no retry: no retry (matches `lambda.destination.delivery-retry` = `none`, 4.6). Invoked 16:54:21Z; queue re-created about 16:55:26Z; 10 receives over about 10 minutes (01-receive-01..10, each 200 `{}`) got nothing. 02 `DestinationDeliveryFailures` Sum 1.0 at 16:55:00Z, one datapoint.
