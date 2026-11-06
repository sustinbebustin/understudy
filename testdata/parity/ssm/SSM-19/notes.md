# SSM-19 notes

- Date: 2026-11-03 (UTC)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- Recipe: docs/research/ssm-parameters.md 13.20; session script changes marked `# session:` (exact-name deletes replace `ssmsweep`, which deletes by prefix and other recordings share P)

- 01 default page (switch `ssm.paging.default-page-size` by path = 10): matches: 10 entries and a NextToken with 12 direct children; `sub/d` not included (one level).
- 01 order (switch `ssm.list.order` = name-ascending): differs: page 1 holds k01, k03-k07, k09-k12, ascending within the page, but k02 and k08 are left for a later page, so the order is not name-ascending across pages.
- 02 path without leading slash: ValidationException `The parameter doesn't meet the parameter name requirements. The parameter name must begin with a forward slash "/". It can't be prefixed with \"aws\" or \"ssm\" (case-insensitive). It must use only letters, numbers, or the following symbols: . (period), - (hyphen), _ (underscore). Special characters are not allowed. All sub-paths, if specified, must use the forward slash symbol "/". Valid example: /get/parameters2-/by1./path0_.` (backslashes before the inner quotes are in the message).
- 03 Name filter: InvalidFilterKey with key `Message`: `The following filter key is not valid: Name. Valid filter keys include: [Type, KeyId, Label].`
- 04 Label BeginsWith: InvalidFilterOption, key `message`: `The following filter option is not valid: BeginsWith. Valid options include: [Equals].`
- 05 empty: `{"Parameters":[]}`: sends the empty list.
- 06 Label filter: returns version 1 (value `01`) and no `Selector`: matches understudy.
- The item's exit=1 is the trailing `ssmdel` grep; every step ran.
