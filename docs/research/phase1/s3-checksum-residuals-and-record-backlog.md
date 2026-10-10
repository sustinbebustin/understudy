# S3 checksum residuals and the record-mode backlog

Scope: gap G8. Part 1 settles the S3 xxHash digest encoding (and the SHA512/MD5 questions) from the AWS-owned CRT and SDK sources. Part 2 is one de-duplicated, ranked list of every UNVERIFIED item in `docs/research/phase1/*.md` that only a human recording against real AWS can settle, each with an exact request script. No agent ran record mode; every real-AWS statement below is either quoted from a cited page or marked UNVERIFIED.

Evidence tags: SRC = AWS-owned open source read at a pinned commit; DOC = AWS documentation fetched 2026-10-08; CALC = computed locally and cross-checked (never in the repo); UNVERIFIED = no primary source observed.

---

# Part 1. S3 checksum residuals

## 1.1 Verdict

| Question | Answer | Status |
|---|---|---|
| Seed | 0. Every SDK calls the CRT constructor without a seed and the CRT defaults it to 0 | SRC |
| Digest bytes | XXHASH64: 8 bytes, XXH64 value big-endian. XXHASH3: 8 bytes, XXH3-64 value big-endian. XXHASH128: 16 bytes, `high64` big-endian then `low64` big-endian (the xxHash canonical form) | SRC |
| Base64 form | Standard alphabet, padded, of those raw bytes (same `BaseChecksum.b64digest` path as every other algorithm) | SRC |
| Header names | `x-amz-checksum-xxhash64`, `x-amz-checksum-xxhash3`, `x-amz-checksum-xxhash128` (lower-cased algorithm id appended to `x-amz-checksum-`) | SRC + DOC |
| Trailer names | Same strings, in `x-amz-trailer` and as the trailer chunk header name, for every algorithm an SDK can compute | SRC |
| Does live S3 accept the XXHASH and SHA512 names in a trailer? | The user guide lists only five trailer values; the newer names are not documented either way | UNVERIFIED (item R08) |
| Does live S3 expect exactly this encoding? | The API pages say "Base64 encoded, 64-bit `XXHASH64` checksum" and "128-bit `XXHASH128`", and the CRT is AWS's own client, so a mismatch would break boto3, Java and C++ against S3. Not observed on the wire | strong inference; UNVERIFIED live |
| SDKs that compute XXHASH* | botocore (needs `awscrt` >= 0.31.2), Java v2 (needs `aws-crt` on the classpath), C++. Not Go v2, JS v3 or Rust | SRC |
| SDKs that compute MD5 as a flexible checksum | None (see 1.5) | SRC |

## 1.2 The CRT chain (what `finalize()` returns)

- The C library (repo `awslabs/aws-checksums`; note the task text says `aws/aws-c-checksums`, which does not exist) vendors Cyan4973/xxHash and documents the contract: "Hash is written out in big-endian, i.e. network order." ([xxhash.h L52-L58](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/include/aws/checksums/xxhash.h#L52-L58), pinned commit `12cd22e8`, 2026-09-30).
- `s_finalize_XXH64` and `s_finalize_XXH3_64` call `aws_byte_buf_write_be64(out, hash)`; `s_finalize_XXH3_128` writes `hash.high64` then `hash.low64`, both with `write_be64` ([xxhash.c L227-L252](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/xxhash.c#L227-L252)). The one-shot `aws_xxhash64_compute`, `aws_xxhash3_64_compute`, `aws_xxhash3_128_compute` do the same ([L392-L456](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/xxhash.c#L392-L456)).
- Streaming state is created with `XXH64_reset(state, seed)`, `XXH3_64bits_reset_withSeed` and `XXH3_128bits_reset_withSeed`, i.e. the default secret with a seed ([xxhash.c L279, L312, L345](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/xxhash.c#L279)). With seed 0 the XXH3 variants use the 192-byte default `kSecret` unmodified.
- `awscrt.checksums.XXHash` (aws-crt-python `48ce5e12`, 2026-10-02): `new_xxhash64(seed: int = 0)`, `new_xxhash3_64(seed = 0)`, `new_xxhash3_128(seed = 0)`, and `finalize() -> bytes` returns the C buffer unchanged ([checksums.py L118-L175](https://github.com/awslabs/aws-crt-python/blob/48ce5e1215be270642d66ce3616daeb3423349bb/awscrt/checksums.py#L118-L175)). The upstream tests pin the bytes for `"Hello world"`: XXH64 `c500b0c912b376d8`, XXH3-64 `b6acb9d84a38ff74`, XXH3-128 `7351f89812f97382b91d05b31e04dd7f` ([test_checksums.py L221-L266](https://github.com/awslabs/aws-crt-python/blob/48ce5e1215be270642d66ce3616daeb3423349bb/test/test_checksums.py#L221-L266)); my local implementation and `awscrt` 0.37.0 reproduce them (CALC).

## 1.3 What each SDK does

| SDK (pinned commit) | Computes | How | Header / trailer name | Source |
|---|---|---|---|---|
| botocore `bfb412fa` (2026-10-07) | CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512, XXHASH64, XXHASH3, XXHASH128. No MD5 | `CrtXxhash64Checksum` wraps `crt_checksums.XXHash.new_xxhash64()`, `...new_xxhash3_64()`, `...new_xxhash3_128()` with no seed; `digest()` is `finalize()`; `b64digest()` is `base64.b64encode(digest())`; XXHash classes registered only if `has_minimum_crt_version((0, 31, 2))`; without CRT it raises `MissingDependencyException` | `"name": f"x-amz-checksum-{algorithm_name}"`; trailer path sets `X-Amz-Trailer` to that name, for any supported algorithm | [httpchecksum.py L54-L66, L131-L164, L355-L398, L458-L478, L631-L679](https://github.com/boto/botocore/blob/bfb412faf00c249a54816e287a09a8bb79442c65/botocore/httpchecksum.py#L131-L164) |
| aws-sdk-java-v2 `87f8087a` (2026-10-07) | CRC32, CRC32C, CRC64NVME, SHA1, SHA256, SHA512, XXHASH*; MD5 exists in `SdkChecksum` but is rejected as a request algorithm | `XxHashChecksum` uses aws-crt-java `XXHash.newXXHash64()/newXXHash3_64()/newXXHash3_128()`; those call the native constructor with seed 0 and `digest()` is "hash as bytes in big endian"; header and trailer value is `BinaryUtils.toBase64(checksumBytes)` | `"x-amz-checksum-" + algorithmId.toLowerCase(Locale.US)`; the trailer provider emits `(checksumName, base64)` for any `SdkChecksum` | [XxHashChecksum.java L26-L60](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/checksums/src/main/java/software/amazon/awssdk/checksums/internal/XxHashChecksum.java#L26-L60), [SdkChecksum.java L47-L59](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/checksums/src/main/java/software/amazon/awssdk/checksums/SdkChecksum.java#L47-L59), [ChecksumUtil.java L60-L62](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/http-auth-aws/src/main/java/software/amazon/awssdk/http/auth/aws/internal/signer/util/ChecksumUtil.java#L60-L62), [ChecksumTrailerProvider.java L49-L59](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/http-auth-aws/src/main/java/software/amazon/awssdk/http/auth/aws/internal/signer/chunkedencoding/ChecksumTrailerProvider.java#L49-L59), [aws-crt-java XXHash.java L46, L138-L145](https://github.com/awslabs/aws-crt-java/blob/4ccd83c24dad2229772d6ac5121ba4906ba8ce93/src/main/java/software/amazon/awssdk/crt/checksums/XXHash.java#L46) |
| aws-sdk-cpp `a9f0ffcc` (2026-10-06) | crc64nvme, crc32, crc32c, sha512, sha256, sha1, xxhash64, xxhash3, xxhash128 (a 9-entry map). No MD5 | `CrtXXHashImpl` wraps `aws-crt-cpp` `XXHash::CreateXXHash64()/CreateXXHash3_64()/CreateXXHash3_128()` (seed defaults to 0 in the CRT-C++ header) and digests into an 8- or 16-byte buffer; the interceptor base64-encodes it | `"x-amz-checksum-" + algorithmName` | [ChecksumInterceptor.h L205-L242, L85](https://github.com/aws/aws-sdk-cpp/blob/a9f0ffcc276664b475230206fa554841ad5bc3de/src/aws-cpp-sdk-core/include/smithy/client/features/ChecksumInterceptor.h#L205-L242), [XXHash.cpp L15-L16, L90-L115](https://github.com/aws/aws-sdk-cpp/blob/a9f0ffcc276664b475230206fa554841ad5bc3de/src/aws-cpp-sdk-core/source/utils/checksum/XXHash.cpp#L90-L115), [aws-crt-cpp XXHash.h L21-L70](https://github.com/awslabs/aws-crt-cpp/blob/f4b8dfd95b593a92de469151544bccc4d39dc58c/include/aws/crt/checksum/XXHash.h#L21-L70) |
| aws-sdk-rust `0d890415` / smithy-rs `38327c6d` | CRC32, CRC32C, CRC64NVME, SHA1, SHA256 only. The S3 crate has `ChecksumAlgorithm::{Md5, Sha512, Xxhash128, Xxhash3, Xxhash64}` enum variants (generated from the model) but no hasher | `aws_smithy_checksums::ChecksumAlgorithm::from_str` accepts crc32, crc32c, sha1, sha256, md5, crc64nvme; "MD5 is now an alias for the default Crc32"; SHA512 and xxhash strings return `UnknownChecksumAlgorithmError` | trailer is `x-amz-trailer` + `x-amz-checksum-<name>` | [smithy-rs lib.rs L29-L110](https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b428c5425fee2989d8da2c0e3a64b/rust-runtime/aws-smithy-checksums/src/lib.rs#L29-L110), [aws-sdk-rust _checksum_algorithm.rs L60-L72](https://github.com/awslabs/aws-sdk-rust/blob/0d8904155d9bc81467715efca821a7bcf4ce5669/sdk/s3/src/types/_checksum_algorithm.rs#L60-L72) |
| aws-sdk-go-v2 `ab2110b0`, aws-sdk-js-v3 `0317bb82` | Already recorded: Go computes CRC32/CRC32C/CRC64NVME/SHA1/SHA256/SHA512; JS computes CRC32/CRC32C/CRC64NVME/SHA1/SHA256; neither computes XXHASH* or MD5 | see [sigv4-checksums-endpoints.md 4.2](sigv4-checksums-endpoints.md) | | [algorithms.go L46-L53](https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03b3990bfd50208a8a05df1d8c5eac/service/internal/checksum/algorithms.go#L46-L53) |

Consequence for response validation: on GET/HEAD botocore prefers `xxhash128, xxhash3, crc64nvme, xxhash64, crc32c, crc32, sha1, sha256, sha512` ([httpchecksum.py L667-L679](https://github.com/boto/botocore/blob/bfb412faf00c249a54816e287a09a8bb79442c65/botocore/httpchecksum.py#L667-L679)); Java skips XXHASH validation when the CRT class is absent and skips MD5 always ([HttpChecksumUtils.java L364-L374](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/sdk-core/src/main/java/software/amazon/awssdk/core/internal/util/HttpChecksumUtils.java#L364-L374)); C++ validates all nine ([ChecksumInterceptor.h L270-L294](https://github.com/aws/aws-sdk-cpp/blob/a9f0ffcc276664b475230206fa554841ad5bc3de/src/aws-cpp-sdk-core/include/smithy/client/features/ChecksumInterceptor.h#L270-L294)). So an understudy that stores an XXHASH object and echoes the stored value on GET with `x-amz-checksum-mode: ENABLED` must emit exactly these digests or a CRT-equipped botocore/C++ client raises a mismatch.

## 1.4 Reference vectors

Input definitions: `empty` is zero bytes; `hello` and `123456789` are the ASCII strings; `patN` is `N` bytes where `byte[i] = i mod 251` (251 is prime, so the pattern never aligns with the 64-byte stripe or 1024-byte block of XXH3). Seed 0. `hex` is the raw big-endian digest; `b64` is the wire value.

Method (CALC): a from-scratch Go implementation of XXH64, XXH3-64 and XXH3-128 (default secret, seed 0, all length classes including the >240 byte scramble path), kept in a scratch directory outside the repo; its output is byte-identical, for all 63 computed vectors (the three named inputs and 18 lengths from 1 byte to 1 MiB, each for three algorithms), to both the Python `xxhash` 4.0.1 package (libxxhash 0.8.3) and `awscrt` 0.37.0 `XXHash.compute_*` plus the streaming `update`/`finalize` path in 4099-byte chunks (the CRT is the primary reader). Reproduce with `uv run --with xxhash --with awscrt python -I ref.py` where `ref.py` is:

```python
import base64, xxhash
from awscrt import checksums as crt
def pat(n): return bytes(i % 251 for i in range(n))
for label, d in [("empty", b""), ("hello", b"hello"), ("123456789", b"123456789"), ("pat1048576", pat(1 << 20))]:
    for alg, ref, one, mk in [
        ("XXHASH64",  xxhash.xxh64(d).digest(),     crt.XXHash.compute_xxhash64(d),     crt.XXHash.new_xxhash64),
        ("XXHASH3",   xxhash.xxh3_64(d).digest(),   crt.XXHash.compute_xxhash3_64(d),   crt.XXHash.new_xxhash3_64),
        ("XXHASH128", xxhash.xxh3_128(d).digest(),  crt.XXHash.compute_xxhash3_128(d),  crt.XXHash.new_xxhash3_128)]:
        h = mk()
        for i in range(0, len(d), 4099): h.update(d[i:i + 4099])
        assert h.finalize() == one == ref
        print(label, alg, ref.hex(), base64.b64encode(ref).decode())
```

### 1.4.1 Required vectors

| Input | Algorithm | hex | b64 |
|---|---|---|---|
| empty | XXHASH64 | `ef46db3751d8e999` | `70bbN1HY6Zk=` |
| empty | XXHASH3 | `2d06800538d394c2` | `LQaABTjTlMI=` |
| empty | XXHASH128 | `99aa06d3014798d86001c324468d497f` | `maoG0wFHmNhgAcMkRo1Jfw==` |
| `hello` | XXHASH64 | `26c7827d889f6da3` | `JseCfYifbaM=` |
| `hello` | XXHASH3 | `9555e8555c62dcfd` | `lVXoVVxi3P0=` |
| `hello` | XXHASH128 | `b5e9c1ad071b3e7fc779cfaa5e523818` | `tenBrQcbPn/Hec+qXlI4GA==` |
| `123456789` | XXHASH64 | `8cb841db40e6ae83` | `jLhB20DmroM=` |
| `123456789` | XXHASH3 | `72dcb18b67a17dff` | `ctyxi2ehff8=` |
| `123456789` | XXHASH128 | `33119477ede5dcd5e9716427681d5860` | `MxGUd+3l3NXpcWQnaB1YYA==` |
| pat1048576 (1 MiB) | XXHASH64 | `89ac0399c4464a31` | `iawDmcRGSjE=` |
| pat1048576 (1 MiB) | XXHASH3 | `6e0d7ac36b8c10ff` | `bg16w2uMEP8=` |
| pat1048576 (1 MiB) | XXHASH128 | `53738d98098cabba6e0d7ac36b8c10ff` | `U3ONmAmMq7puDXrDa4wQ/w==` |
| `Hello world` (CRT upstream test) | XXHASH64 / XXHASH3 / XXHASH128 | `c500b0c912b376d8` / `b6acb9d84a38ff74` / `7351f89812f97382b91d05b31e04dd7f` | |

Note for XXH3-128 that for 1-3 byte and for 241+ byte inputs `low64` equals the XXH3-64 value (compare `pat1` below), which is a property of the algorithm, not a copy error.

### 1.4.2 Length-class vectors (hex only)

One row per branch of the XXH3 length dispatch (0, 1-3, 4-8, 9-16, 17-128, 129-240, 241+ with and without a full 1024-byte block), so a port can be tested branch by branch.

| N | XXHASH64 | XXHASH3 | XXHASH128 |
|---|---|---|---|
| 1 | `e934a84adb052768` | `c44bdff4074eecdb` | `a6cd5e9392000f6ac44bdff4074eecdb` |
| 3 | `e5c7bb4533bc65dd` | `5f4299fc161c9cbb` | `e3b55f57945a17cf5f4299fc161c9cbb` |
| 4 | `ffced8604453cc1e` | `60dab036a58211f2` | `eb70bf5fc779e9e6a6111d53e80a3db5` |
| 8 | `884a173614b81b8d` | `3a1c2d7c85af88f8` | `e1e4432a62217fe4cfd50c61c8bb98c1` |
| 9 | `67d85784a7c78c5b` | `e9612598145bb9dc` | `16c769d83e4aebce907931979dca3746` |
| 16 | `44b6ef2fb84169f7` | `8355e3a6f61770db` | `72950631827607e2842812cc870dcae2` |
| 17 | `5603e60c527599b6` | `9ef341a99de37328` | `685bc458b37d057fc06e233df7729217` |
| 128 | `7a7fe14647b9ab92` | `85c6174c7ff4c46b` | `14792fc3af88dc6c05321a0b64d67b41` |
| 129 | `0ba25dfd6e891fcf` | `ec7642b431ba3e5a` | `dd5e74ac6b45f54ebc30b63382b09a3b` |
| 240 | `012947f0da6a27b1` | `375a384d957fe865` | `65b5be86da5540e7c92b68e16f83bbb6` |
| 241 | `8d643f23bf2808e1` | `02e8cd95421c6d02` | `1da1cb61bcb8a2a102e8cd95421c6d02` |
| 1023 | `d66738f081c25cf4` | `d3d91d80ac495685` | `4325711b0ed4d742d3d91d80ac495685` |
| 1024 | `138e26c65048ce29` | `e5d78bafa45b2aa5` | `d0ac1f7b93bf57b9e5d78bafa45b2aa5` |
| 1025 | `cfd73aedd2d6a39d` | `e95c42288f28186e` | `2882ebca04ec915ce95c42288f28186e` |
| 4096 | `122a8c8d994ad3ec` | `7135ffa504f1bc71` | `e12cd72144990fe57135ffa504f1bc71` |

(The same run also checked N = 2, 128-boundary neighbours, 2048 and 1 MiB; all agree.)

### 1.4.3 Companion vectors for the other new algorithms and a composite hypothesis

CALC with Python `hashlib` (b64 of the raw digest):

| Input | SHA512 | MD5 |
|---|---|---|
| `hello` | `m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw==` | `XUFAKrxLKna5cZ2REBfFkg==` |
| pat1048576 | `Z9rVae78mGo7JCT1UW1aAoS7U9e1LXX17YgaaDCpV2XMyCvEh1L7aTQiV58R3JpABWHsGIWvnu73A9u9MS1P0A==` | `jyk6L2wZs0UVL3pJu0xkPA==` |

Two-part multipart fixture for item R08: part 1 = pat5242880 (5 MiB), part 2 = pat1048576. Per-part b64 digests and the composite hypothesis "checksum of the concatenated raw part digests, then `-2`" (the rule stated for the older algorithms in [s3.md 6.4](s3.md) and [sigv4-checksums-endpoints.md 4.4](sigv4-checksums-endpoints.md); for the new algorithms it is UNVERIFIED):

| Alg | part 1 | part 2 | composite hypothesis |
|---|---|---|---|
| XXHASH64 | `nt7qU/z8nB4=` | `iawDmcRGSjE=` | `9GhH8zktinM=-2` |
| XXHASH3 | `f7lM6WcDDeA=` | `bg16w2uMEP8=` | `f6EQWtiO3o8=-2` |
| XXHASH128 | `N+DwsSPHJSV/uUzpZwMN4A==` | `U3ONmAmMq7puDXrDa4wQ/w==` | `Ger1rESaX/PiNtq26MVKfQ==-2` |
| SHA512 | `UUcj5aGVOfG4H6SovYePw1sFP9roSxo6w6etwJlWkni7M72Zg5eKqAAgQv+F3SB1Yet8+jJEY8j2ZtKv22VTPw==` | (SHA512 of pat1048576 above) | `hbWfVq2dROxQPWBMcw1EZgLLOpNmP/zfonMOFFvvC1LaQbYma4kY9a3JmG7oMa9BgHj2grxxoS0u7FKeSlg7vQ==-2` |
| MD5 | `TChkDcjfGTOq6hkhANUK4A==` | `jyk6L2wZs0UVL3pJu0xkPA==` | `TcMA1YYQbcbHRS4dun66LA==-2` |

## 1.5 SHA512 and MD5

- SHA512: header `x-amz-checksum-sha512`, base64 of the raw 64-byte digest. botocore uses `hashlib.sha512` ([httpchecksum.py L189-L200, L631-L636](https://github.com/boto/botocore/blob/bfb412faf00c249a54816e287a09a8bb79442c65/botocore/httpchecksum.py#L189-L200)); Java uses `DigestAlgorithmChecksum(SHA512)` ([SdkChecksum.java L48-L49](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/checksums/src/main/java/software/amazon/awssdk/checksums/SdkChecksum.java#L48-L49)); C++ `Sha512` ([ChecksumInterceptor.h L218-L221](https://github.com/aws/aws-sdk-cpp/blob/a9f0ffcc276664b475230206fa554841ad5bc3de/src/aws-cpp-sdk-core/include/smithy/client/features/ChecksumInterceptor.h#L218-L221)); Go v2 `AlgorithmSHA512`. The API page describes it as "Base64 encoded, 512-bit `SHA512` digest" ([API_CompleteMultipartUpload](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html)).
- MD5 (correction to [s3.md 6.1](s3.md), which said "enum only in Go"): the model and the API pages have `x-amz-checksum-md5` ("Base64 encoded, 128-bit `MD5` digest", same page), but no SDK sends it as a flexible request checksum. Java: `isHttpChecksumCalculationNeeded` throws `IllegalArgumentException("MD5 is not supported. Please use a different checksum algorithm or provide a pre-calculated MD5 value.")` and `shouldSkipAlgorithm` skips MD5 response validation ([HttpChecksumUtils.java L264-L270, L370-L372](https://github.com/aws/aws-sdk-java-v2/blob/87f8087a446c51b0d05c06ef387712a6ce674b04/core/sdk-core/src/main/java/software/amazon/awssdk/core/internal/util/HttpChecksumUtils.java#L264-L270)). botocore: `md5` is not in `_CHECKSUM_CLS`, so it raises `FlexibleChecksumError("Unsupported checksum algorithm: md5")` ([httpchecksum.py L354-L365, L631-L636](https://github.com/boto/botocore/blob/bfb412faf00c249a54816e287a09a8bb79442c65/botocore/httpchecksum.py#L354-L365)). Rust: `from_str("md5")` returns CRC32 ([lib.rs L70-L72](https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b428c5425fee2989d8da2c0e3a64b/rust-runtime/aws-smithy-checksums/src/lib.rs#L70-L72)). C++ has no md5 entry. MD5 arrives at understudy only from a hand-built request or a precomputed `ChecksumMD5` value. Treat it as accept, verify (stdlib `crypto/md5`), store, return.
- Do not confuse `x-amz-checksum-md5` with `Content-MD5`: the user guide says "Additionally, you can provide a checksum with each request using the Content-MD5 header" ([DOC](https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html#using-additional-checksums)).

## 1.6 What S3 does per the docs (no live observation)

All quotes from [checking-object-integrity-upload](https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html) and [API_CompleteMultipartUpload](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CompleteMultipartUpload.html), fetched 2026-10-08.

- Ten algorithms: CRC64NVME, CRC32, CRC32C, SHA1, SHA256, MD5, XXHASH64, XXHASH3, XXHASH128, SHA512.
- Multipart type matrix: CRC64NVME full-object only; CRC32 and CRC32C both; SHA1, SHA256, MD5, XXHASH64, XXHASH3, XXHASH128, SHA512 composite only. "Full object checksums in multipart uploads are only available for CRC-based checksums."
- "When using multipart uploads with the new checksum algorithms (MD5, XXHash3, XXHash64, XXHash128, SHA-512), you must specify the checksum algorithm in the `CreateMultipartUpload` request using the `x-amz-checksum-algorithm` header. If you provide a checksum value in the `CompleteMultipartUpload` request (for example, `x-amz-checksum-sha512`) without having specified the algorithm in `CreateMultipartUpload`, the request will fail with an `InvalidRequest` error." For CRC32, CRC32C, SHA-1, SHA-256 the same situation is "currently accepted but not validated or stored".
- `CompleteMultipartUpload` request headers: `x-amz-checksum-{crc32,crc32c,crc64nvme,sha1,sha256,sha512,md5,xxhash64,xxhash3,xxhash128}`, `x-amz-checksum-type` (`COMPOSITE | FULL_OBJECT`; a type that differs from the Create is `BadDigest`), `x-amz-mp-object-size` (mismatch is `HTTP 400 InvalidRequest`). Request body `Part` elements carry `ChecksumCRC32 ... ChecksumXXHASH128`; the result carries the same ten elements plus `ChecksumType`.
- Non-consecutive part numbers on a composite upload: "Amazon S3 generates an `HTTP 500 Internal Server` error."
- Trailers: "The following `x-amz-trailer` values are currently supported: `x-amz-checksum-crc32`, `x-amz-checksum-crc32c`, `x-amz-checksum-crc64nvme`, `x-amz-checksum-sha1`, `x-amz-checksum-sha256`." SHA512, MD5 and XXHASH names are absent from that list and from every example. The same section says the trailer header name must equal the `x-amz-trailer` value ("if a request contains `x-amz-trailer: x-amz-checksum-crc32` and the trailer chunk has the header name `x-amz-checksum-sha1`, the request fails") and that the value is "a base64 encoding of the big-endian checksum value for that object". Whether the list is stale or a real restriction is UNVERIFIED (R08).
- Multipart trailers: the doc says SDKs "automatically create trailing checksums for chunked uploads" and "S3 generates checksum values for every part"; `UploadPart` is named as a supported aws-chunked request ("`PutObject` and `UploadPart` requests with trailing checksums").
- Copies: "With a copy command, the checksum of the object is a direct checksum of the full object."

## 1.7 Decisions for understudy that follow

1. Implement all ten algorithms in `internal/s3/checksum`; the three xxHash variants need a hand-port (no stdlib). Seed 0, default secret, canonical big-endian output, XXH3-128 as `high64 || low64`. Use 1.4 as golden vectors, plus a streaming-equals-one-shot property test with random chunking (the CRT's own tests do streaming and one-shot, [xxhash_test.c](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/tests/xxhash_test.c)). This removes the "return unimplemented for XXHASH*" proposal in s3.md and go-toolchain.md.
2. Accept the three names (and sha512, md5) in headers and in `x-amz-trailer` plus the trailer chunk. The docs list only five trailer names, but the SDKs that can compute the others send them (1.3) and an emulator that rejected them would break botocore/Java/C++ callers that asked for those algorithms. Record the choice as "behavior from SDK source, not AWS-recorded" in the coverage report until R08 runs.
3. Multipart: enforce the matrix in 1.6 (composite only for MD5, XXHASH*, SHA512) with the `InvalidRequest` texts already recorded in [s3.md 6.4](s3.md); implement the `InvalidRequest` on Complete-time checksum without Create-time algorithm for the five new algorithms; composite value per the hypothesis in 1.4.3 until R08 confirms.
4. XXH3 streaming needs the full 192-byte secret and the 1024-byte block scramble; port from the xxHash reference ([xxhash.h in aws-checksums](https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/external/xxhash.h), the canonical spec is [xxhash_spec.md](https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md)) and validate every length class in 1.4.2.

## 1.8 Other residuals settled while reading source

| Item (origin) | Result | Source |
|---|---|---|
| CloudWatch wire protocol per SDK ([smithy-protocol-tests.md](smithy-protocol-tests.md) open item; [tf-compute.md 11](tf-compute.md)) | Go v2: `rpcv2.NewCBOR`; JS v3: `AwsJson1_0Protocol` with `awsQueryCompatible: true`; botocore: `awsJson1_0` (CORRECTED (G1): this said rpcv2Cbor; `resolved_protocol` returns `json` and boto3 1.43.109 and AWS CLI 2.36.38 were wire-captured, [wire-cbor.md section 2.1](wire-cbor.md), consistent with [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md)). A CBOR codec is a phase 1 requirement. Both files corrected in place | [Go api_client.go L215](https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03b3990bfd50208a8a05df1d8c5eac/service/cloudwatch/api_client.go#L215), [JS runtimeConfig.shared.ts L36-L44](https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abed1302aaae350a8bbfa40f60ef32d/clients/client-cloudwatch/src/runtimeConfig.shared.ts#L36-L44) |
| Which SDKs name the trailer for non-CRC algorithms ([sigv4-checksums-endpoints.md 9](sigv4-checksums-endpoints.md)) | botocore, Java and C++ all derive the trailer or header name as `x-amz-checksum-<lowercase algorithm>` with no allow-list | 1.3 |
| Default seed in Java and C++ | 0 | [aws-crt-java XXHash.java L46-L48](https://github.com/awslabs/aws-crt-java/blob/4ccd83c24dad2229772d6ac5121ba4906ba8ce93/src/main/java/software/amazon/awssdk/crt/checksums/XXHash.java#L46-L48), [aws-crt-cpp XXHash.h L60-L70](https://github.com/awslabs/aws-crt-cpp/blob/f4b8dfd95b593a92de469151544bccc4d39dc58c/include/aws/crt/checksum/XXHash.h#L60-L70) |

Still open and needing no real AWS (capture the SDK's bytes against a local listener): which client sends the `\n\r\n` trailer terminator versus `\r\n\r\n` (Java and Rust not captured here), and Java v2 / PHP default S3 addressing with a custom endpoint. See 2.5.

---

# Part 2. Record-mode backlog

## 2.1 How this list was built

All 19 files were scanned for `UNVERIFIED`, "open question", "Gaps", "not recorded" and "unconfirmed"; each file's own register was read in full ([sqs.md 12](sqs.md), [lambda-events.md 11](lambda-events.md), [lambda-api.md](lambda-api.md), [secrets-logs-events-alarms.md 8](secrets-logs-events-alarms.md), [tf-messaging.md 8](tf-messaging.md), [tf-compute.md 11](tf-compute.md), [tf-storage.md](tf-storage.md), [wire-rest.md](wire-rest.md), [wire-json-query.md](wire-json-query.md), [smithy-models.md](smithy-models.md), [smithy-protocol-tests.md](smithy-protocol-tests.md), [sigv4-checksums-endpoints.md 9](sigv4-checksums-endpoints.md), [landscape.md](landscape.md), [go-toolchain.md 18](go-toolchain.md), [containers.md 14.4](containers.md), [dynamodb-local.md](dynamodb-local.md), plus every inline UNVERIFIED in [s3.md](s3.md), [sns.md](sns.md), [iam-sts-kms.md](iam-sts-kms.md)). 473 lines across the 19 files match those scan terms (`rg -c -i` over the terms, summed; many restate the same item or are already resolved); they collapse into the 18 backlog items below. Items that are decisions, licence questions or container/OS facts are not record work; they are listed in 2.5.

Ranking is by blast radius on the phase 1 workload, highest first:
1. Wrong answer breaks the SNS to SQS to Lambda pipeline (message loss, duplicate processing, stuck mappings).
2. Wrong shape makes `terraform apply` fail or the second `terraform plan` show a diff.
3. Wrong behavior on a hot path that SDKs tolerate (S3 uploads, DynamoDB lock table, filter policies, log reads).
4. Wrong error text or status for rarely hit paths.

## 2.2 Common conventions for every script

- Run only in a throwaway sandbox AWS account with budget alarms, from a shell with your own credentials; nothing here is meant for an agent. Region `us-east-1` unless an item says otherwise. All resource names start with `$P=usr-rec-$(date +%Y%m%d)`; every item ends with its own teardown.
- Capture the raw exchange for every call, not the parsed output: method, URL, request headers (redact `Authorization`, `X-Amz-Security-Token`), request body, response status, all response headers, response body bytes (base64 for CBOR or binary), local timestamps. Use the parity harness's record mode when it exists; until then `aws --debug ... 2>&1 | tee` (shows request and response with headers and body) or an HTTPS logging proxy. The wire is the product here; the CLI's pretty output hides the bytes that matter (empty body vs `{}`, key order, `&quot;`).
- Store each item's capture as `<item-id>/<step>.http` plus a one-line `notes.md` stating the AWS date, region and CLI/SDK versions. Mark every field in the Expected column as "matches", "differs: <what>", or "n/a".
- `jq`, `base64`, `curl >= 7.75` (for `--aws-sigv4`), AWS CLI v2 and Python with `uv` are assumed.
- "Hypothesis" is what the research files currently assume; the decision rule says what to change if AWS differs.

## 2.3 Ranked index

| ID | Item | Blast radius | Origin |
|---|---|---|---|
| R01 | SQS receipt-handle lifecycle: stale handle after re-receive, delete and change-visibility edges | Pipeline: every Lambda delete and retry | sqs.md 12, 6.2, 13.10 |
| R02 | SQS DLQ and redrive: move timing, message IDs, receive counts, move-task shapes and errors | Pipeline: poison-message path | sqs.md 12, 9; tf-messaging.md 8 |
| R03 | Lambda SQS mapping runtime: ChangeMessageVisibility use, FIFO plus batching window, validation texts, partial-batch edge cases, states | Pipeline: the consumer itself | lambda-events.md 11 (1-6, 11) |
| R04 | DynamoDB lock-table surface: TTL and PITR describe/update shapes, error JSON, `x-amz-crc32` | Lock table and every Terraform refresh of the table | dynamodb-local.md, wire-json-query.md, sigv4 9 |
| R05 | Terraform plan stability, messaging: SQS/SNS attribute bytes and defaults | Terraform second plan | tf-messaging.md 8, sqs.md, sns.md |
| R06 | Terraform plan stability, S3: new-bucket subresource states, not-configured statuses, S3 Control tags | Terraform second plan; every S3 bucket | s3.md 4.1, wire-rest.md 3.6, tf-storage.md, landscape.md |
| R07 | Terraform plan stability, compute and ops: Lambda, EventBridge, CloudWatch alarms, Secrets read shapes | Terraform second plan | tf-compute.md 11, tf-storage.md, secrets-logs-events-alarms.md |
| R08 | S3 SHA512/MD5/XXHASH acceptance: header, trailer, multipart composite | S3 uploads using non-default algorithms (CRT clients) | this file, sigv4 9, s3.md 6 |
| R09 | SNS filter-policy semantics battery | Pipeline routing | sns.md 6, 9 |
| R10 | CloudWatch Logs filter-pattern token rules, paging, PutLogEvents limits | Log read-back | secrets-logs-events-alarms.md Q1, Q2, Q11 |
| R11 | JSON/Query wire edges: error codes, empty bodies, unknown members, `x-amzn-query-mode` | Every SDK error assertion | wire-json-query.md, smithy-models.md, sqs.md 12 |
| R12 | S3 object and multipart edges: ranges, Complete retry, DeleteObjects limit, ETag quoting | Managed uploads and retries | s3.md, wire-rest.md |
| R13 | Lambda Invoke, function URL and runtime edges | One function URL; Invoke errors | lambda-api.md, lambda-events.md 11 (9), wire-rest.md |
| R14 | EventBridge schedule grammar and event edges | One scheduled rule | secrets-logs-events-alarms.md Q4, lambda-events.md 11 (10) |
| R15 | KMS, Secrets Manager, IAM, STS error texts and SSE-KMS failure surfaces | Error text; S3 SSE-KMS failure path | iam-sts-kms.md, secrets-logs-events-alarms.md Q2, Q3, Q8 |
| R16 | SNS delivery shapes and validation texts | Enveloped delivery bytes; rarely hit errors | sns.md, lambda-events.md 11 (7) |
| R17 | S3 event notification fields | Should-have feature | lambda-events.md 11 (8), s3.md 7 |
| R18 | SigV4 time windows and presign failures | Presign tests | sigv4-checksums-endpoints.md 9, s3.md |

## 2.4 Scripts

Shell variables used below: `A=$(aws sts get-caller-identity --query Account --output text)`, `R=us-east-1`, `Q=https://sqs.$R.amazonaws.com/$A`.

### R01. SQS receipt-handle lifecycle

Why: Lambda deletes by handle after every successful batch, and every retry re-receives; the emulator must decide what a stale handle does. Docs say a delete with an older handle may succeed without deleting; no recording exists ([sqs.md 6.2, 12, 13.10](sqs.md)).

Script:
1. `aws sqs create-queue --queue-name $P-std --attributes VisibilityTimeout=5` and `aws sqs create-queue --queue-name $P-f.fifo --attributes FifoQueue=true,ContentBasedDeduplication=true,VisibilityTimeout=5`.
2. Standard queue: `send-message --message-body m1`; `receive-message --visibility-timeout 5 --attribute-names All --wait-time-seconds 2` -> handle H1; `sleep 6`; receive again -> H2 (same MessageId). Record both full responses (ApproximateReceiveCount must be 2).
3. `delete-message --receipt-handle H1` while H2 is current. Record status, body. Then `receive-message --wait-time-seconds 2` immediately: is the message gone (H1 deleted it), still invisible, or visible? Wait 6 s and receive again. Finally `delete-message --receipt-handle H2`. Record every outcome.
4. Repeat step 2, then `delete-message --receipt-handle H2` first, then `delete-message --receipt-handle H1` (stale and message already gone). Record.
5. Repeat step 2 and call `change-message-visibility --receipt-handle H1 --visibility-timeout 30` (stale), then with H2 (current), then after deleting the message (deleted). Record the single-call error code, `__type`, message and `x-amzn-query-error` for each.
6. Batch variants: `delete-message-batch` and `change-message-visibility-batch` with entries `[H1(stale), H2(current), garbage, a handle from another queue]`. Record `Successful`/`Failed` entries (Id, SenderFault, Code, Message) and the HTTP status.
7. FIFO queue (group `g`): send m1, receive H1, `sleep 6`, `delete-message H1` (expired, not re-received: the existing recording says `InvalidParameterValue ... The receipt handle has expired`), then receive again H2 and `delete-message H1`, then `delete-message H2`. Record texts.
8. Record the handle strings themselves (length, alphabet, whether two receives of the same message share a prefix) for 5 receives.

Expected / decision: hypothesis in [sqs.md 6.2](sqs.md): standard queue stale handle succeeds as a no-op-or-delete. If step 3 shows the message deleted, make "any handle for an existing message deletes it" the rule; if it shows still invisible, keep per-receive handles and treat stale as silent success; if an error, copy code and text. Record whether single-call and batch use the same code (open item "single-call versus batch code").

Teardown: delete both queues.

### R02. SQS DLQ and redrive

Why: the pipeline's poison-message path; [sqs.md 9.2, 12](sqs.md) and the docs disagree about IDs and counts.

Script:
1. Queues: `$P-dlq`, `$P-src` with `RedrivePolicy={"deadLetterTargetArn":"<dlq arn>","maxReceiveCount":"2"}`, `VisibilityTimeout=1`. Also FIFO pair `$P-dlq.fifo`/`$P-src.fifo`.
2. Send 3 messages to `$P-src` with a body and two message attributes (one String, one Binary); record the three `MessageId`/`MD5OfMessageBody`.
3. For each message: receive (`--attribute-names All --message-attribute-names All --visibility-timeout 1`), wait 2 s, receive again, wait 2 s, then receive a third time. Record every response including `ApproximateReceiveCount`, `SentTimestamp`, `ApproximateFirstReceiveTimestamp`. Record what the third receive returns and when (immediately, or only after a later poll).
4. Poll `$P-dlq` at 0, 1, 5, 30 s after the third receive attempt with `--attribute-names All`: record `MessageId` (same as the source message?), `ApproximateReceiveCount` (reset?), `SentTimestamp` vs original, `DeadLetterQueueSourceArn`, `MD5OfBody`, attributes retained.
5. `get-queue-attributes --attribute-names All` on both: `ApproximateNumberOfMessages*` over time during step 4.
6. `aws sqs start-message-move-task --source-arn <dlq arn>` (no destination): record `TaskHandle`, base64-decode it. `list-message-move-tasks --source-arn <dlq arn> --max-results 10` immediately and after completion (all fields: Status, ApproximateNumberOfMessagesMoved, ApproximateNumberOfMessagesToMove, FailureReason, StartedTimestamp, DestinationArn, MaxNumberOfMessagesPerSecond). Receive from `$P-src`: new `MessageId`? `ApproximateReceiveCount`? `SentTimestamp`? attributes? `DeadLetterQueueSourceArn` gone?
7. Errors, record status/`__type`/message each: `start-message-move-task` on a queue never used as DLQ; on a deleted queue ARN; a second task while one is RUNNING; `--max-number-of-messages-per-second 501`; `--destination-arn` pointing to a missing queue; `cancel-message-move-task` on a COMPLETED handle; `set-queue-attributes` with a RedrivePolicy naming a missing DLQ, an invalid ARN, `maxReceiveCount` 0 and 1001; a RedrivePolicy from FIFO source to standard DLQ; set `RedriveAllowPolicy={"redrivePermission":"denyAll"}` on the DLQ and then set the source's RedrivePolicy (message text for "denied").
8. FIFO pair: repeat steps 2-6 using `--message-group-id g1 --message-deduplication-id d1..d3`; in the DLQ also record `MessageGroupId`, `MessageDeduplicationId`, `SequenceNumber`.

Expected / decision: hypotheses ([sqs.md 9, 12](sqs.md)): move happens at the receive attempt that exceeds `maxReceiveCount`, ID and count carried, redrive via move task gives new IDs per the docs ("considered new messages with a new `messageID` and `enqueueTime`"). Update the SQS message record and the move-task state machine to whatever is recorded.

Teardown: delete all four queues.

### R03. Lambda SQS mapping runtime

Why: this is the consumer. Open items: Lambda's use of `ChangeMessageVisibility`, FIFO plus window, validation texts, partial-batch edge cases, `LastProcessingResult` ([lambda-events.md 11 items 1-6, 11](lambda-events.md)).

Setup: role `$P-role` trusted by `lambda.amazonaws.com` with `AWSLambdaSQSQueueExecutionRole`; function `$P-fn` (python3.13) whose handler logs `json.dumps(event)` with `time.time()` and returns a response read from the first record's body (a JSON `{"mode": "..."}`), so one function covers all steps. Queues `$P-q` (VisibilityTimeout 60), `$P-q.fifo`.

Script:
A. ChangeMessageVisibility use and retry timing. Mode `raise`. `create-event-source-mapping --function-name $P-fn --event-source-arn <q> --batch-size 1`. Wait for `Enabled`. Send one message. Record the function-log timestamps of every invocation for 5 minutes. If redelivery is at about 60 s after the first invocation the mapping does not change visibility; any other value means it does (record the delta). Also record the message's `ApproximateReceiveCount` at each invocation (from `event.Records[0].attributes`). Optionally check CloudTrail event history (`aws cloudtrail lookup-events --lookup-attributes AttributeKey=EventSource,AttributeValue=sqs.amazonaws.com`) and note which SQS actions appear for the mapping's role; whether SQS data-plane calls appear at all is UNVERIFIED.
B. States. Poll `get-event-source-mapping` every second for 30 s after step A's create and after `update-event-source-mapping --no-enabled` and `--enabled`: record `State`, `StateTransitionReason`, `LastProcessingResult`, `LastModified` at each tick, and the time from `Creating` to `Enabled`.
C. Validation matrix. Each as a separate create or update; record status, `__type`, message verbatim: FIFO queue with `--batch-size 11`; FIFO with `--maximum-batching-window-in-seconds 5` (accepted? echoed in `get`?); standard with `--batch-size 11` and no window; `--batch-size 10001`; `--maximum-batching-window-in-seconds 301`; `--maximum-concurrency 1` and `1001`; `--scaling-config` together with `--provisioned-poller-config`; 6 filters in `--filter-criteria`; a malformed filter; duplicate mapping for the same function and queue; nonexistent queue; nonexistent function; `--function-response-types ReportBatchItemFailures ReportBatchItemFailures`.
D. Partial batch. Mapping with `--function-response-types ReportBatchItemFailures --batch-size 5 --maximum-batching-window-in-seconds 5`; send 5 messages; function returns, in successive runs (mode in the first record body): `{"batchItemFailures":[{"itemIdentifier":"<id2>"}]}`; `{"BatchItemFailures":[...]}`; `{"batchItemFailures":[{"ItemIdentifier":"<id2>"}]}`; `{"batchItemFailures":[{"itemIdentifier":"unknown"}]}`; `{"batchItemFailures":null}`; `"not json"`; `{}`. After each, count messages deleted vs visible at 70 s (`get-queue-attributes`). Then repeat the first variant on a mapping without `ReportBatchItemFailures`.
E. Payload. For one standard message with attributes and one FIFO message, store the full `event` JSON: is `AWSTraceHeader` present, `DeadLetterQueueSourceArn` after a redrive, `md5OfMessageAttributes` null vs absent, FIFO keys.
F. Function deleted while mapping exists: `delete-function`, then `get-event-source-mapping` (State, still present?), send a message and see whether it is received.

Expected / decision: hypotheses in [lambda-events.md](lambda-events.md) (no CMV, FIFO window accepted and ignored, case-sensitive keys, mapping survives function deletion). Copy recorded texts into the validation table and flip the corresponding "choose X" decisions in lambda-events.md 28.

Teardown: delete mappings, function, role, queues, log group.

### R04. DynamoDB lock-table surface

Why: the shim answers TTL and continuous-backups itself, and Terraform's `aws_dynamodb_table` reads them on every refresh; Local is not AWS ([dynamodb-local.md](dynamodb-local.md)).

Script:
1. `aws dynamodb create-table --table-name $P-lock --attribute-definitions AttributeName=LockID,AttributeType=S --key-schema AttributeName=LockID,KeyType=HASH --billing-mode PAY_PER_REQUEST`, then `wait table-exists`.
2. Immediately record the full responses of `describe-table`, `describe-time-to-live`, `describe-continuous-backups`, `list-tags-of-resource --resource-arn <arn>`, `describe-contributor-insights`.
3. `update-time-to-live --time-to-live-specification Enabled=true,AttributeName=ExpiresAt`: record the response; `describe-time-to-live` at 0, 5, 30, 120 s (`ENABLING` then `ENABLED`?). Repeat the same update (already enabled), then `Enabled=false` (record), repeat disable, then enable again immediately (the "modified multiple times within a fixed interval" error), and an invalid attribute name (empty, 256+ chars).
4. `update-continuous-backups --point-in-time-recovery-specification PointInTimeRecoveryEnabled=true` and with `RecoveryPeriodInDays=1` and `36`; `describe-continuous-backups` each time (fields `ContinuousBackupsStatus`, `PointInTimeRecoveryStatus`, `EarliestRestorableDateTime`, `LatestRestorableDateTime`, `RecoveryPeriodInDays`); repeat enable; disable.
5. Lock semantics: `put-item --item '{"LockID":{"S":"a"},"ExpiresAt":{"N":"100"}}' --condition-expression 'attribute_not_exists(LockID) OR ExpiresAt < :now' --expression-attribute-values '{":now":{"N":"200"}}'` (succeeds), again with `:now` 50 (fails). Record the `ConditionalCheckFailedException` response: status, `__type`, `message` key casing, headers. Repeat with `--return-values-on-condition-check-failure ALL_OLD`.
6. Headers: for steps 2, 3 and 5 record whether `x-amz-crc32` is present on success and on every error response, and whether it equals the CRC32 of the exact body bytes.
7. Error shapes: `describe-time-to-live --table-name does-not-exist` (`ResourceNotFoundException` text), `update-time-to-live` on a missing table, `describe-continuous-backups` on a missing table.
8. Optional, 48 h: put an item with `ExpiresAt` one hour in the past, poll `get-item` every 10 minutes, record the time to deletion (reference only; the emulator uses its own clock).

Expected / decision: replace the shim's invented JSON with the recorded bytes; the repeat-update and enable-while-enabled error texts are the listed unknowns.

Teardown: `delete-table`.

### R05. Terraform plan stability, messaging

Why: Terraform diffs attribute strings; [tf-messaging.md](tf-messaging.md) shows a spaced `RedrivePolicy` from moto produced exit 2 on the second plan.

Script:
1. `create-queue --queue-name $P-a --attributes file://a.json` where `a.json` has `RedrivePolicy` as the pretty-printed string `{"maxReceiveCount": 3, "deadLetterTargetArn": "<dlq arn>"}` (reversed key order, spaces, number not string) and `RedriveAllowPolicy` `{"redrivePermission": "byQueue", "sourceQueueArns": ["<arn>"]}` and a `Policy` with spaces. Then `get-queue-attributes --attribute-names All`: record the exact byte string of each of the three (compact? sorted? number or string for `maxReceiveCount`?).
2. Defaults: `create-queue` with no attributes, standard and FIFO: record the complete attribute map (`MaximumMessageSize`, `SqsManagedSseEnabled`, `ReceiveMessageWaitTimeSeconds`, `DelaySeconds`, `MessageRetentionPeriod`, FIFO keys). Create with `KmsMasterKeyId=alias/aws/sqs`: record `SqsManagedSseEnabled`. FIFO: record `SqsManagedSseEnabled`.
3. `set-queue-attributes --attributes Policy=,RedrivePolicy=` (empty strings): record status and whether the keys then disappear from `get-queue-attributes`.
4. Real SQS error `x-amzn-query-error` on a 5xx: not reproducible; skip.
5. SNS: `create-topic --name $P-t`, then `get-topic-attributes`: record `Policy` (bytes, spacing, `Id`), `DeliveryPolicy`/`EffectiveDeliveryPolicy` (bytes), `DisplayName`, `SubscriptionsConfirmed`. `subscribe --protocol sqs --attributes FilterPolicy='{ "a": ["x"] }',RawMessageDelivery=true,RedrivePolicy='{"deadLetterTargetArn": "<arn>"}'` then `get-subscription-attributes`: record the exact bytes of `FilterPolicy`, `RedrivePolicy`, the value of `RawMessageDelivery`, `FilterPolicyScope`, `PendingConfirmation`, `ConfirmationWasAuthenticated`. `list-subscriptions-by-topic` before and after the SQS queue policy is set (when does the ARN stop being `PendingConfirmation`; for SQS it should auto-confirm).
6. Cross-account SQS subscription confirmation: skip (two accounts needed); UNVERIFIED stays.

Expected / decision: change the emulator to echo whichever normalisation AWS applies; hypothesis in tf-messaging.md is compact sorted JSON for the redrive attributes.

Teardown: delete topic and queues.

### R06. Terraform plan stability, S3

Why: `aws_s3_bucket` and its sub-resources read a dozen subresources; the statuses and bytes of the "not configured" cases decide whether refresh succeeds ([s3.md 4.1](s3.md), [wire-rest.md 3.6](wire-rest.md)).

Script (all via `aws s3api` with `--debug` or the harness; bucket `$P-s3` in us-east-1, a second `$P-s3w` in us-west-2):
1. Create both buckets. Immediately GET every subresource and record status, headers, body: `?acl`, `?policy`, `?policyStatus`, `?cors`, `?website`, `?lifecycle`, `?replication`, `?object-lock`, `?tagging`, `?logging`, `?versioning`, `?accelerate`, `?requestPayment`, `?encryption`, `?ownershipControls`, `?publicAccessBlock`, `?notification`, `?location`, `?analytics` (list), `?inventory` (list), `?metrics` (list), `?intelligent-tiering` (list), and each of those four with `id=nope`.
2. `delete-bucket-encryption`, then `get-bucket-encryption` (error `ServerSideEncryptionConfigurationNotFoundError` with which status, or the default rule again?). `put-bucket-encryption` with SSE-KMS naming a nonexistent key, a disabled key, a pending-deletion key and an alias that does not exist (does S3 validate the key?). Then put a valid KMS rule and `get-bucket-encryption`: is `KMSMasterKeyID` echoed as sent (bare id, alias, ARN)? With `BucketKeyEnabled` omitted.
3. `delete-public-access-block`, `get-public-access-block` (status, body), `delete-bucket-ownership-controls`, `get-bucket-ownership-controls`.
4. Exact bytes of `GetBucketAcl` and `GetBucketRequestPayment` on an untouched bucket.
5. `create-bucket` in us-east-1 with `LocationConstraint=EU`; then `get-bucket-location`, `list-buckets` (`BucketRegion` for that bucket), `head-bucket` (`x-amz-bucket-region`).
6. S3 Control tagging as Terraform does it: `aws s3control tag-resource --account-id $A --resource-arn arn:aws:s3:::$P-s3 --tags Key=k,Value=v --debug` (capture the request XML root element and the request path), `list-tags-for-resource` (response shape), `untag-resource`; `list-tags-for-resource` for a bucket ARN you do not own and for a missing bucket.
7. `x-amz-storage-class` on a `put-object` followed by `head-object` and `get-object` (omitted for STANDARD?) and in `list-objects-v2`.

Expected / decision: tables in s3.md 4.1 and wire-rest.md 3.6; any difference becomes the emulator's golden response.

Teardown: delete both buckets.

### R07. Terraform plan stability, compute and ops

Why: each item below is a Terraform read-after-write that either diffs or errors if the emulator's shape differs ([tf-compute.md 11](tf-compute.md), [tf-storage.md](tf-storage.md), [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md)).

Script:
1. Lambda config: `create-function --environment 'Variables={A=1}'`; `update-function-configuration --environment 'Variables={}'` then `get-function-configuration` (is `Environment` omitted, `{}`, or `{"Variables":{}}`?). Immediately after `update-function-code` call `publish-version` and `update-function-configuration` (the in-progress `ResourceConflictException`: record status and message). `delete-function`: record the HTTP status. `get-function-code-signing-config --function-name` for a function with none (empty string or omitted `CodeSigningConfigArn`).
2. Lambda permissions: `add-permission --principal 123456789012 --action lambda:InvokeFunction --statement-id a`, then `get-policy`: is `Principal` rewritten to `{"AWS":"arn:aws:iam::123456789012:root"}`? Also `--principal '*'`, `--principal sns.amazonaws.com --source-arn <topic>`. `get-policy` on a function with no policy and `remove-permission` for a missing statement (two different texts).
3. ESM clearing: create a mapping with `--filter-criteria` and `--scaling-config MaximumConcurrency=5`, then `update-event-source-mapping --filter-criteria '{}'` and `--scaling-config '{}'`; `get-event-source-mapping`: are the keys omitted or empty objects? Record the mapping ARN format and `UUID`.
4. EventBridge: `put-rule --name $P-r --event-pattern '{ "source": ["a.b"],   "detail-type": ["x"] }'` (extra spaces), `describe-rule` (byte-exact `EventPattern`), `list-rules`; `describe-rule --name missing`, `describe-event-bus --name missing`, `delete-rule` missing, `remove-targets` missing: record `ResourceNotFoundException` texts.
5. CloudWatch alarms: `aws cloudwatch put-metric-alarm --alarm-name $P-al --metric-name Errors --namespace AWS/Lambda --statistic Sum --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanOrEqualToThreshold` (the CLI speaks rpcv2Cbor; record the response body as base64 and decode it), then `describe-alarms --alarm-names $P-al`: which optional fields appear unprompted (`DatapointsToAlarm`, `Unit`, `TreatMissingData`, `ActionsEnabled`, `StateReason`, `StateTransitionedTimestamp`, `AlarmArn` format). Repeat once from a JS SDK v3 script (JSON 1.0 wire) to compare. Tags via `tag-resource`/`list-tags-for-resource`.
6. Secrets: `create-secret --name $P-s1 --secret-string x --kms-key-id <bare key id>`, again with the key ARN, the alias name, and `alias/aws/secretsmanager`; `describe-secret`: what `KmsKeyId` holds in each case, and whether it is absent with no key given.

Expected / decision: fix each emulator output to the recorded shape; where a field was assumed "omitted", state it in the golden file.

Teardown: delete the alarm, rules, function, mapping, secrets (`--force-delete-without-recovery`).

### R08. S3 SHA512, MD5 and XXHASH acceptance

Why: Part 1 settled the client-side bytes; only a live run settles whether S3 accepts them, in which positions, and what composite it stores.

Setup: `uv run --with boto3 --with awscrt python` (awscrt >= 0.31.2). Bucket `$P-cks`. Test bodies: `hello` and the pat1048576 body of 1.4. Expected values: 1.4.1, 1.4.3.

Script:
1. Header form. `put_object(Bucket, Key, Body=b"hello", ChecksumXXHASH64="JseCfYifbaM=")` and the same for `ChecksumXXHASH3="lVXoVVxi3P0="`, `ChecksumXXHASH128="tenBrQcbPn/Hec+qXlI4GA=="`, `ChecksumSHA512="m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="`, `ChecksumMD5="XUFAKrxLKna5cZ2REBfFkg=="` (if botocore rejects the member name, send the raw header with an `before-call` handler). Record status, response `x-amz-checksum-*`, `x-amz-checksum-type`, ETag. Then `head_object(ChecksumMode="ENABLED")`, `get_object_attributes(ObjectAttributes=["Checksum"])` and `list_objects_v2` (does `Contents[].ChecksumAlgorithm` list the new name), and record the stored algorithm and value.
2. Trailer form via the SDK (HTTPS makes botocore pick the trailer): `put_object(Body=io.BytesIO(body), ChecksumAlgorithm="XXHASH64")` and likewise XXHASH3, XXHASH128, SHA512 for both bodies. Record the on-the-wire request (Content-Encoding, `X-Amz-Trailer`, `x-amz-content-sha256`, trailer chunk bytes) and S3's response. Also `aws s3api put-object --body f --checksum-algorithm XXHASH64` (CLI ships the CRT).
3. Raw trailer matrix (algorithms an SDK cannot send). Use a tiny signer: build the request with `botocore.awsrequest.AWSRequest(method="PUT", url=..., data=b"")`, `x-amz-content-sha256: STREAMING-UNSIGNED-PAYLOAD-TRAILER`, `Content-Encoding: aws-chunked`, `x-amz-decoded-content-length: 5`, `x-amz-trailer: <name>`; sign with `SigV4Auth(creds, "s3", region).add_auth(req)`; send `5\r\nhello\r\n0\r\n<name>:<b64>\r\n\r\n` with `http.client` over TLS. Cases (name:value): `x-amz-checksum-crc32` (control), `x-amz-checksum-sha256` (control), `x-amz-checksum-md5`, `x-amz-checksum-sha512`, `x-amz-checksum-xxhash64`, `-xxhash3`, `-xxhash128` (each with the correct `hello` value), the same names with a wrong value (use another algorithm's digest), `x-amz-checksum-nope`, `x-amz-trailer` naming crc32 while the chunk carries sha1 (docs: fails), and each correct case once with the `\n\r\n` terminator and once with `\r\n\r\n`. Record status and the full error body for each.
4. Header errors: two checksum headers (`xxhash64` plus `crc32`); `x-amz-sdk-checksum-algorithm: XXHASH64` with no value header; wrong-length and non-base64 values for `xxhash128` and `sha512` (compare with the existing sha256 text "Value for x-amz-checksum-sha256 header is invalid."); wrong value (BadDigest text per algorithm: is the name upper-cased as "The XXHASH64 you specified did not match the calculated checksum."?); checksum header plus a wrong `Content-MD5` (which error wins).
5. Multipart. For each of XXHASH64, XXHASH3, XXHASH128, SHA512, MD5: `create_multipart_upload(ChecksumAlgorithm=alg)`; (a) with `ChecksumType="COMPOSITE"` and (b) in a second run with `"FULL_OBJECT"` (record the error text for the new algorithms); upload part 1 = pat5242880 and part 2 = pat1048576 with `ChecksumAlgorithm=alg` (botocore cannot compute MD5; pass `ChecksumMD5=` from 1.4.3); record each `UploadPart` response checksum; `complete_multipart_upload` with the parts and their checksums. Compare the Complete result's `Checksum<ALG>` and `ChecksumType` with the composite hypothesis in 1.4.3; `head_object(PartNumber=1, ChecksumMode="ENABLED")`, `get_object_attributes(ObjectAttributes=["Checksum","ObjectParts"])` for per-part values.
6. Doc-stated failures: `create_multipart_upload` without an algorithm, upload parts, then `complete_multipart_upload` with `ChecksumXXHASH64=<some value>` (docs: `InvalidRequest`; record the message); the same for a CRC32 header (docs: accepted, not stored; verify nothing is returned); Create with XXHASH64 but parts uploaded without checksums, then Complete with and without part checksums; Complete with `x-amz-mp-object-size` wrong.
7. Copy: `copy_object(ChecksumAlgorithm="XXHASH3", CopySource=...)` from a CRC64NVME object; record `CopyObjectResult.ChecksumXXHASH3` and compare with the direct digest of the body.
8. Unknown algorithm name in `create_multipart_upload` (`ChecksumAlgorithm="FOO"` via raw header): record the text ("Checksum algorithm provided is unsupported..." per [s3.md 6.4](s3.md)).

Expected / decision: hypothesis in Part 1 (accepted everywhere, composite = hash of concatenated digests). If trailers with the new names are rejected, make understudy reject them with the recorded text and drop decision 2 of 1.7; if the composite differs, record the recipe.

Teardown: abort any open uploads, empty and delete the bucket.

### R09. SNS filter-policy semantics

Why: the pipeline routes on attribute- and body-scope filter policies; the research settled most grammar from docs but left these open ([sns.md 6, 9](sns.md)).

Setup: topic `$P-t`; for each case one SQS queue `$P-fN` with the queue policy allowing the topic, subscription with `RawMessageDelivery=true` and the `FilterPolicy` and `FilterPolicyScope` given. For each case: `subscribe` (record accept or the exact error), `publish` the message, `receive-message --wait-time-seconds 5` (delivered Y/N). Message attributes in `--message-attributes` form.

| # | Scope | Policy | Message | Question |
|---|---|---|---|---|
| 1 | attr | `{"p":[100]}` | attr `p` Number `100` | bare number rule against a Number attribute |
| 2 | attr | `{"p":[{"numeric":["=",100]}]}` | attr `p` Number `100.0` | numeric equality formatting |
| 3 | attr | `{"p":[100]}` | attr `p` String `"100"` | string vs number |
| 4 | body | `{"p":[100]}` | `{"p":"100"}` | string vs number in body scope |
| 5 | body | `{"p":["100"]}` | `{"p":100}` | the reverse |
| 6 | attr | `{"t":[{"exists":false}]}` | no attributes at all | exists:false with zero attributes (docs say no match) |
| 7 | attr | `{"t":[{"anything-but":{"suffix":"s"}}]}` | attr `t` String `a` | accepted now? (rejected in a 2024 recording) |
| 8 | attr | `{"t":[{"anything-but":["a","b"]}]}` | attr `t` String.Array `["a","c"]` | any-element vs all-elements |
| 9 | attr | `{"t":[{"equals-ignore-case":"AB"}]}` | attr `t` String `ab` | Event Ruler extension accepted? |
| 10 | body | `{"a":{"b":["x"]},"c":["y"]}` | `{"a":{"b":"x"},"c":"y"}` | baseline for 11 and 12 |
| 11 | body | 6 leaf keys spread over 2 nested objects | any | leaf-key counting vs top-level counting (limit 5) |
| 12 | body | `{"$or":[{"a":["x"]},{"b":["y"]}],"c":["z"],"d":["1"],"e":["2"],"f":["3"]}` | any | how `$or` branches count toward the 5-key limit |
| 13 | body | `{"a":[{"prefix":"x"}],"b":[{"suffix":"y"}]}` with 4 wildcards | any | complexity limit text and order of checks |
| 14 | body | `{"o":{"a":["1"],"b":["2"]}}` | `{"o":[{"a":"1"},{"b":"2"}]}` | sibling keys across array elements |
| 15 | body | `{"p":[{"numeric":[">",1e9]}]}` | `{"p":2e9}` | numeric range limit |
| 16 | attr | a policy over 256 KB | n/a | oversize error text and status |
| 17 | attr | `{"a.b":["x"]}` | attr `a.b` String `x` | dotted attribute names in attr scope |
| 18 | body | `{"a":["x"]}` | `[{"a":"x"}]` (array root), and a plain non-JSON string | non-object bodies |
| 19 | attr | `{"p":["100"]}`, then `{"p":[{"anything-but":"100"}]}` | attr `p` Number `100` | string and anything-but rules against a Number attribute (switch `sns.filter-policy.number-attribute-type`) |

Also, for the whole topic, `set-subscription-attributes --attribute-name FilterPolicy` with a nested policy under attribute scope: record the error text and whether the Subscribe form carries the `Attributes Reason:` prefix.

Expected / decision: switches listed in [sns.md 449](sns.md); set each from the recording and update the coverage report note from "docs/emulators" to "AWS-recorded".

Teardown: delete subscriptions, topic, queues.

### R10. CloudWatch Logs filter patterns and paging

Why: the brief requires real FilterLogEvents pattern syntax; unquoted-term boundaries have no AWS-recorded test ([secrets-logs-events-alarms.md Q1, Q2, Q11](secrets-logs-events-alarms.md)).

Script:
1. `create-log-group --log-group-name /usr/$P`, `create-log-stream --log-stream-name s1`.
2. `put-log-events` 14 events 1 s apart with these messages: `ERROR`, `ERRORS`, `ERROR:`, `[ERROR]`, `MY_ERROR`, `error`, `an ERROR occurred`, `ERROR-1`, `ERROR.x`, `ERRORx`, `status=ERROR`, `{"level":"ERROR","n":5}`, `{"level":"INFO","n":2}`, `a b ERROR c`.
3. `filter-log-events --filter-pattern P` for each P and record the matched message list: `ERROR`, `"ERROR"`, `?ERROR ?INFO`, `ERROR -an`, `ERROR occurred`, `"an ERROR"`, `{ $.level = "ERROR" }`, `{ $.n > 3 }`, `{ $.level = "ERROR" && $.n >= 5 }`, `{ $.missing NOT EXISTS }`, `[a, b, c]` (space-delimited), `ERR*`, `*ROR`, an invalid pattern `{ $.n = }` (error text and status).
4. Boundaries: events at timestamps T, T+1, T+2 ms; `filter-log-events --start-time T --end-time T+1` and `--start-time T+1`: inclusivity of both ends; `get-log-events --start-time/--end-time` the same.
5. Paging: 7 events, `filter-log-events --limit 3` then follow `nextToken` until empty; record the `searchedLogStreams` array and whether an empty page with a token occurs. `get-log-events --limit 3 --start-from-head`, record `nextForwardToken` stability at the end.
6. `put-log-events` limits: events spanning more than 24 h, older than 14 days, newer than 2 h ahead, out-of-order timestamps, an empty message, a 256 KiB+ message, 10001 events. `put-retention-policy --retention-in-days 6`. Record status, `__type`, message each.
7. A Lambda function logging once: `describe-log-streams` for its group (stream name format `YYYY/MM/DD/[$LATEST]<32 hex>`; and the `[N]` form for a published version) - shares a run with R13.

Expected / decision: replace the "whole-token" interpretation with the recorded matrix; error texts into the constants table.

Teardown: delete the log group.

### R11. JSON and Query wire edges

Why: every SDK-facing assertion on error codes and empty bodies; [wire-json-query.md](wire-json-query.md) lists a dozen items only a real endpoint settles.

Use `curl --aws-sigv4 "aws:amz:$R:<service>" --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" -H "x-amz-security-token: $AWS_SESSION_TOKEN" -i` against real endpoints. SQS JSON: `-H 'Content-Type: application/x-amz-json-1.0' -H 'X-Amz-Target: AmazonSQS.<Op>' -d '<json>' https://sqs.$R.amazonaws.com/`.

Script (record status, all headers, raw body for each):
1. SQS `GetQueueUrl` for a missing queue, once with `-H 'x-amzn-query-mode: true'` and once without: is `x-amzn-query-error` emitted both times; the `__type` form (`com.amazonaws.sqs#QueueDoesNotExist` vs short).
2. SQS validation failures not modelled: `ReceiveMessage` with `MaxNumberOfMessages` 0 and 11, `WaitTimeSeconds` 21, `VisibilityTimeout` 43201; `SendMessage` with `DelaySeconds` 901 and an empty body; `CreateQueue` with `MaximumMessageSize` 1023 and `VisibilityTimeout` -1: status, `__type`, message.
3. Unknown member (`{"QueueUrl":"...","Foo":1}`), an integer sent as `5.0`, a string where an integer is expected, `Content-Type: application/x-amz-json-1.1` on an SQS (1.0) call, a missing `Content-Type`, an unknown `X-Amz-Target` operation, a wrong target prefix.
4. No-output operations: SQS `DeleteMessage` and `SetQueueAttributes` (JSON) and `DeleteMessage` (Query): empty body or `{}`; status. Empty lists: `ListQueues` with a prefix matching nothing (JSON `{}` vs `{"QueueUrls":[]}`), SNS `ListTopics` empty (Query XML `<Topics/>`?), `ListSubscriptions`, SQS `ReceiveMessage` on an empty queue.
5. Query: unknown `Action` (SQS and SNS), SQS Query `NonExistentQueue` body (the `xmlns` host: `queue.amazonaws.com` vs `sqs.amazonaws.com`), `Detail` element contents in the error header, `Accept: application/json` on a Query call.
6. DynamoDB unknown `X-Amz-Target`, and a `DynamoDB_20120810.DescribeTable` with a body that has a duplicate member name.
7. DynamoDB Streams SigV4 service name: sign `streams.dynamodb.$R.amazonaws.com` as `dynamodb`; if rejected, retry as `streams.dynamodb`.

Expected / decision: whichever is recorded becomes a protocol-layer golden test; where AWS is lenient (unknown members, `5.0`) keep the emulator strict only if the brief's strict-validation promise requires it and document the divergence.

### R12. S3 object and multipart edges

Why: boto3 `upload_fileobj`/multipart retries and ranged reads hit these ([s3.md 5-6](s3.md)).

Script (bucket `$P-edge`; use `aws s3 presign` URLs with `curl -i` for raw requests):
1. Object `r` = ASCII `0123456789`. Ranges: `bytes=0-1,4-5` (multi-range: 200 full, 206, or error), `bytes=abc`, `bytes=5-2`, `bytes=-0`, `bytes=0-`, `bytes=100-` (416 body), `items=0-1`, `bytes=0-1,`, an empty `Range:`. Record status, `Content-Range`, body.
2. `get-object --part-number 1` on the single-part object; `--part-number 2` (416 `InvalidPartNumber` body fields); `head-object --part-number 1`.
3. `CompleteMultipartUpload` repeated: complete an upload, then POST the identical Complete body again, and once with a changed part list; record status, body, ETag.
4. `delete-objects` with 1001 keys; `Quiet=true`; a key that does not exist; a duplicate key in one request.
5. `list-objects-v2 --max-keys 0`, `--max-keys abc`, `--max-keys 1001`, `--start-after` beyond the last key.
6. Start a multipart upload (do not complete), `delete-bucket` on an otherwise empty bucket: `BucketNotEmpty` or success; then `list-multipart-uploads`.
7. Raw `CompleteMultipartUpload` response bytes (via `--debug`): `&quot;` or literal quotes in `<ETag>`; whitespace before `<?xml`.
8. Multipart `Last-Modified`: create the upload, `sleep 30`, complete; `head-object` `Last-Modified` equals initiation or completion time.
9. Limits: key of 1025 bytes (`KeyTooLongError` text), user metadata over 2 KiB (`MetadataTooLarge` text), GET with `response-content-type` on an unsigned request, `upload-part-copy` with malformed `x-amz-copy-source-range` (`bytes=5-2`, `bytes=0-99999999`).
10. `put-object` with a wrong `Content-MD5` and a wrong `x-amz-checksum-crc32` together: which error wins.
11. Object Lock bucket (`--object-lock-enabled-for-bucket`) with a default retention: `put-object` without `Content-MD5`/checksum (the enforcement error text).

Expected / decision: the cited UNVERIFIED lines in s3.md 5-6 and the S3 error table become recorded.

Teardown: abort uploads, remove objects (use `--bypass-governance-retention` where needed), delete bucket.

### R13. Lambda Invoke, function URL and runtime edges

Setup: reuse R03's role; function `$P-url` that returns, per `event.rawPath`, the response variants below and logs the incoming event; `create-function-url-config --auth-type NONE` (record `FunctionUrl`, trailing slash, `url-id` alphabet and length over 10 creations) and `add-permission` for public invoke (record whether `lambda:InvokeFunction` with `--invoked-via-function-url` is also required, per the Oct 2025 change).

Script:
1. Function URL requests (`curl -i`): POST with an empty body (is `body` absent or `""`), `text/plain`, `application/json`, `application/octet-stream`, `image/png` (which are base64: `isBase64Encoded`), duplicate query keys, a `Cookie:` header (`cookies` array), `Content-Encoding: gzip`, a very long path; `OPTIONS` with and without `--cors`; `HEAD`. Store the logged event.
2. Function responses: bare JSON object, a string, a number, `null`, `{"statusCode":99}`, `{"statusCode":200,"body":123}`, `cookies`, multi-value headers, `isBase64Encoded:true` with invalid base64, a function error (`raise`), a timeout (sleep > Timeout), an oversize body (> 6 MB). Record status and body (the 502 body on function error and timeout).
3. `get-function-url-config` on a default-`InvokeMode` URL: is `InvokeMode` echoed.
4. Invoke: `invoke --payload ''`, `--payload 'null'`, `--payload 'not json'` (message), `--invocation-type DryRun` on a Pending function, `--invocation-type Event` status (202) and headers, `X-Amz-Function-Error` value for a thrown error and for a `{"errorMessage"}` return, `X-Amz-Executed-Version` with a weighted alias.
5. Create-function validation texts: environment key `AWS_REGION` (reserved), `--timeout 901`, `--memory-size 10241`, `--memory-size 127`.
6. Versions: `publish-version` twice, `delete-function --qualifier 2`, `publish-version` again: number reuse.
7. Identity inside the function: call `sts get-caller-identity` from the handler (boto3): `Arn` session-name part equals the function name?
8. Log stream name for the invocation: `describe-log-streams` on `/aws/lambda/$P-url` (shared with R10 step 7).

Expected / decision: lambda-api.md and lambda-events.md unknown lists; fix the response-inference rules from recorded cases.

### R14. EventBridge schedule grammar and event edges

Why: one scheduled rule drives a Lambda; the cron grammar's edges decide validity and first-fire time ([secrets-logs-events-alarms.md Q4](secrets-logs-events-alarms.md), [lambda-events.md 11 (10)](lambda-events.md)).

Script:
1. For each expression `put-rule --name $P-r --schedule-expression "<expr>"`, record accept or the exact error (status, `__type`, message), then `describe-rule` to record the stored `ScheduleExpression` (normalised?). Corpus (valid unless noted): `cron(0 12 * * ? *)`, `cron(0/15 * * * ? *)`, `cron(0 18 ? * MON-FRI *)`, `cron(0 12 L * ? *)`, `cron(0 12 LW * ? *)`, `cron(0 12 15W * ? *)`, `cron(0 12 L-3 * ? *)`, `cron(0 12 ? * 2#1 *)`, `cron(0 12 ? * 6L *)`, lower-case `cron(0 12 ? * mon *)`, wrapped hour `cron(0 20-2 * * ? *)`, wrapped weekday `cron(0 12 ? * FRI-MON *)`, out-of-order `cron(0 12 20-10 * ? *)`, impossible `cron(0 0 31 2 ? *)`, `cron(0 12 ? * ? *)` (both `?`), `cron(0 12 * * * *)` (neither `?`), five fields `cron(0 12 * * ?)`, year `cron(0 12 * * ? 1969)` and `2200`, `cron(60 * * * ? *)`, `cron(0 24 * * ? *)`, `cron(0 12 32 * ? *)`, `cron(0 12 * 13 ? *)`, `cron(0 12 ? * 0 *)`, `cron(0 12 ? * 8 *)`, `cron(5/0 * * * ? *)`, `cron(0 12 */2 * ? *)`, leading and double spaces, tab separators, `CRON(...)` upper-case; `rate(1 minute)`, `rate(1 minutes)`, `rate(5 minute)`, `rate(5 minutes)`, `rate(0 minutes)`, `rate(-1 minutes)`, `rate(1.5 hours)`, `rate(1 hour)`, `rate(1 day)`, `rate(7 days)`, `rate(1 week)`, `rate(5  minutes)`, `RATE(5 minutes)`, `rate(5 minutes )`.
2. First-fire observation: SQS target (`put-targets` with the queue ARN and a queue policy allowing `events.amazonaws.com`), rules `rate(1 minute)` and `cron(0/2 * * * ? *)` created at a recorded wall-clock time; receive for 6 minutes; record each message's `time`, `id`, full envelope and arrival time (first fire one period after creation, or at the next aligned minute?). Add `Input` constant and `InputTransformer` variants and record the delivered body. Disable then enable the rule and note re-fire timing.
3. `put-events` edges: `Source` `aws.foo` and `aws.events`, missing `DetailType`, `Detail` not JSON, `Detail` over 256 KiB, an explicit past `Time`, a nonexistent `EventBusName`: record `FailedEntryCount` and each `ErrorCode`/`ErrorMessage`.
4. Rule on a custom bus with a schedule expression (rejected?). EventBridge Scheduler payload for an equivalent schedule (differs from rule payload?).

Expected / decision: build the schedule validity corpus from this table instead of the documented grammar; keep undecided rows flagged.

Teardown: remove targets, delete rules and queue.

### R15. KMS, Secrets Manager, IAM and STS error texts

Why: the pipeline uses SSE-KMS buckets and Secrets Manager; the failure surfaces are the least documented ([iam-sts-kms.md](iam-sts-kms.md), [secrets-logs-events-alarms.md Q2, Q3, Q8](secrets-logs-events-alarms.md)).

Script:
KMS (key `K1`, alias `alias/$P-k`):
1. `disable-key`, then `encrypt`, `decrypt` (with a blob made before disabling), `generate-data-key`, `re-encrypt`, `describe-key`, `get-key-rotation-status`, `enable-key-rotation`, `create-alias`, `put-key-policy`: status, `__type`, message each (`DisabledException` text, ARN form).
2. `enable-key`; `schedule-key-deletion --pending-window-in-days 7`; repeat the same battery plus `cancel-key-deletion`, `schedule-key-deletion` again, `enable-key`, `create-key` window 6 and 31: `KMSInvalidStateException` vs `DisabledException`, exact text.
3. Validation: `encrypt` empty and 4097-byte plaintext, `generate-data-key` with both `--key-spec` and `--number-of-bytes` and with neither, `decrypt` garbage blob, `decrypt` with the wrong `--key-id`, `create-alias` targeting an alias, `alias/aws/x` reserved prefix, a duplicate alias, `put-key-policy` over 32768 characters and with a non-`default` policy name, `list-key-policies`, `get-key-policy` (exact bytes), `describe-key` on a missing key (`NotFoundException` text), `create-key` description default.
4. S3 SSE-KMS failure surface: `put-object --server-side-encryption aws:kms --ssekms-key-id` with a disabled key, a pending-deletion key, a nonexistent key id, a nonexistent alias, a key in another region: S3 status, `Code` and `Message` (e.g. `KMS.DisabledException`?) per case. Same with a bucket default KMS rule and `multipart` upload.
5. Secrets: `create-secret` with a disabled KMS key; duplicate name; `put-secret-value` with the same `--client-request-token` twice with different values; `delete-secret --recovery-window-in-days 6` and `31` and `0`; `delete-secret` twice; `get-secret-value` on a scheduled-for-deletion secret; `restore-secret` on a live secret; names containing `!`, 513 characters; ARN suffix alphabet over 20 creations. Stage trace: `create-secret`, `put-secret-value --version-stages AWSPENDING`, `update-secret-version-stage --move-to-version-id` to `AWSCURRENT`, `describe-secret` after every call (`VersionIdsToStages`).
6. IAM/STS: `create-role` with an invalid trust document, `put-role-policy` with a policy missing `Resource` and one with a duplicate `Sid`, `tag-role` with duplicate keys differing in case and 51 tags, `delete-role` with an attached policy, with an instance-profile membership, `delete-user` with an access key, `create-policy` sixth version, `assume-role` against a role whose trust denies you (`AccessDenied` text), `get-caller-identity` on assumed credentials, `get-access-key-info` on a deleted key.

Expected / decision: replace each third-party text in iam-sts-kms.md with the recorded one, and settle the choice between `DisabledException` and `KMSInvalidStateException` for pending deletion.

Teardown: schedule deletions (7 days), delete secrets, roles, policies, users.

### R16. SNS delivery shapes and validation texts

Script:
1. Topic with a Lambda subscriber (logs `event`) and an SQS subscriber (raw off): publish with `Subject` omitted, with `Subject` set, with String, Number, Binary and String.Array attributes. Record the Lambda `Records[0].Sns` object (is `Subject` `null` or absent, attribute `Type` for Number and String.Array, Binary `Value` encoding, `SignatureVersion`, `SigningCertUrl`, `UnsubscribeUrl`) and the SQS enveloped body bytes (key order, `Timestamp` format, `Signature`, `MessageAttributes` shape).
2. `publish-batch`: 11 entries, duplicate ids, an invalid id, one oversize entry among valid ones, total over 256 KiB, an entry with an invalid attribute, an entry with `Subject` of 101 characters: `Successful`/`Failed` split and error codes.
3. Publish validation: `Subject` 101 chars and with a newline; attribute named `AWS.x`/`Amazon.x`, name 257 characters, Number attribute `abc`, 11 attributes to an SQS raw subscription (discarded silently?), Binary attribute to Lambda.
4. FIFO: FIFO topic to FIFO queue publish with group and dedup ids; record `SequenceNumber` text/length; subscribe a Lambda and an HTTP endpoint to a FIFO topic (error text).
5. Subscribe: twice with the same endpoint and different protocols; `confirm-subscription` twice; wrong token; endpoint protocol mismatch (`http` endpoint without scheme); application protocol with a non-ARN.
6. `delete-topic` twice; subscriptions after topic deletion (the 48 h detached claim: `list-subscriptions` after delete).

Expected / decision: sns.md items marked UNVERIFIED in sections 4, 5, 7, 8; lambda-events.md 11 (7).

### R17. S3 event notification fields

Script: bucket with notifications to SQS (`$P-ev`, queue policy allowing `s3.amazonaws.com` with `aws:SourceArn`/`aws:SourceAccount`), to a Lambda that logs the event, to SNS. Steps:
1. `put-bucket-notification-configuration` with and without the queue policy (record the `InvalidArgument` text), with an invalid ARN, with overlapping prefix filters, a prefix with a space, a filter with both prefix and suffix; capture whether an `s3:TestEvent` message arrives in SQS (and whether none arrives at Lambda).
2. `put-object` (key `a b/ü+1.txt`), multipart upload, `copy-object`, `delete-object`, `delete-objects` (3 keys), on an unversioned and a versioned bucket. Record each event JSON: `eventVersion`, `userIdentity.principalId` form, `configurationId` (generated if omitted?), `s3.object.key` encoding, `size`/`eTag` on removals, `versionId`, `sequencer` format and ordering for two writes to one key, `requestParameters.sourceIPAddress`, `responseElements`.
3. Delivery latency distribution over 20 puts.

Expected / decision: s3.md 7 choices (`eventVersion` 2.1 vs 2.6 and so on).

### R18. SigV4 time windows and presign failures

Script: sign with botocore using a patched clock (set `datetime` in `botocore.auth` or pass `x-amz-date`) and send with `curl -i`:
1. S3 presigned GET with `X-Amz-Expires=604801`, `0`, and `-1`; expired by 1 s; `X-Amz-Date` 16 minutes in the future; an unknown `X-Amz-Algorithm`. Record status and bodies (`AuthorizationQueryParametersError` texts).
2. Header-signed S3 request 14 and 16 minutes skewed (`RequestTimeTooSkewed` fields).
3. SQS (JSON), SNS (Query), STS (Query), DynamoDB with `x-amz-date` 6 and 16 minutes skewed: status, `__type`, message text (`Signature expired: ... is now earlier than ...`).
4. JS-style presigned S3 PUT carrying `x-amz-checksum-crc32=AAAAAA==` in the query: accepted when the body's CRC32 differs?
5. `aws-chunked` trailers with `Expect: 100-continue`: send headers only with a bad signature and see whether S3 answers before reading the body (interaction listed open in wire-rest.md).

Expected / decision: sigv4-checksums-endpoints.md section 9 first bullet.

## 2.5 Not record-mode work

These flagged items are settled with a decision, a licence reading, or a local capture and need no real AWS:

| Item | Where | How to settle |
|---|---|---|
| Which client sends the `\n\r\n` trailer terminator; exact HTTP form of Java v2, JS v3, CLI streaming uploads | sigv4 9, landscape.md | Point each SDK at a local TCP listener and dump the bytes |
| Java v2 and PHP default S3 addressing with a custom endpoint; `*.localhost` on macOS, Windows, musl, scratch images | landscape.md, wire-rest.md | Run the SDKs against a local server; test resolver per platform |
| DynamoDB Local telemetry flag, TTL sweep period, `-dbPath` odd regions, SQLite copy safety, JS `x-amz-crc32` handling | dynamodb-local.md | Run the `amazon/dynamodb-local` image and SDK probes locally |
| Legal reading of "in connection with the Services" for DynamoDB Local; LocalStack snapshot licence; Service Reference JSON licence; Maven publication of the models | dynamodb-local.md, secrets-logs-events-alarms.md Q10, smithy-models.md | Human/legal decision |
| Listener exposure, hosted API vs RIE fallback, clock semantics for idle reaping, virtual-host DNS inside functions, ECR URIs, CPU/memory enforcement, non-root uid, arch mismatch | containers.md 14.4 | ADR decisions; the uid is readable from the public base image |
| Whether `smithy-go` server codegen runs `appliesTo: server` cases; provenance of botocore's protocol corpus | smithy-protocol-tests.md | Read the repositories |
| Reject-vs-ignore for `RESPONSE_STREAM` and FIFO windows; 5/+300/1250 scaling ramp; async payload cap; Function URL host routing | lambda-events.md 28 | Decisions (the FIFO window facts come from R03) |
| Duplicate-name JSON bodies and invalid UTF-8 acceptance by AWS | go-toolchain.md 18 | Partly R11 step 3; the rest is a Smithy compliance-test question |
