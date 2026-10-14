# Go 1.27 toolchain and library research for understudy

Scope: what the installed toolchain (go1.27.1, `$GOROOT/VERSION` time 2026-08-28) and the libraries named for phase 1 actually provide, verified by running code on this machine where possible. Sources: Go release notes, `go doc`/`go help`, `$GOROOT/src`, pkg.go.dev, upstream repos read with `gh api`. Items marked UNVERIFIED were not confirmed against a primary source or by execution.

Scratch programs used for verification lived outside the repo (session scratchpad) and are not part of any deliverable.

## 0. Status table (Go 1.27.1)

| Feature | Status in 1.27 | Since | Source |
|---|---|---|---|
| `testing/synctest` (`Test`, `Wait`, new `Sleep`) | GA; the 1.24 experiment API (`synctest.Run`) is gone | GA 1.25, `Sleep` 1.27 | https://go.dev/doc/go1.25 , https://go.dev/doc/go1.27 , `go doc testing/synctest` |
| `net/http/httptest.NewTestServer` (in-memory network, for synctest) | New | 1.27 | https://go.dev/doc/go1.27 , `go doc net/http/httptest.NewTestServer` |
| `encoding/json/v2` + `encoding/json/jsontext` | On by default (GOEXPERIMENT `jsonv2` is in the baseline); opt-out `GOEXPERIMENT=nojsonv2` is "expected to be removed in a future release"; `encoding/json` v1 is now backed by v2 | experiment 1.25, default-on 1.27 | https://go.dev/doc/go1.27 , `$GOROOT/src/internal/buildcfg/exp.go:87` (`JSONv2: true` in `baseline`) |
| Iterators (`iter`, range-over-func, `maps.Keys`, `slices.Sorted`, `strings.SplitSeq`) | GA | language 1.23, `strings` iterators 1.24 | https://go.dev/doc/go1.24 |
| `net/http.ServeMux` method/wildcard patterns | GA; trailing-slash redirect is now 307 | patterns 1.22, 307 in 1.26 | https://go.dev/doc/go1.26 , verified locally (section 5) |
| `log/slog` | GA; `DiscardHandler` (1.24), `GroupAttrs` + `Record.Source` (1.25), `NewMultiHandler` (1.26) | 1.21 | https://go.dev/doc/go1.24 , https://go.dev/doc/go1.25 , https://go.dev/doc/go1.26 |
| `os.Root` | GA; full method set since 1.25 | 1.24 | https://go.dev/doc/go1.24 , https://go.dev/doc/go1.25 |
| `unique`, `weak`, `runtime.AddCleanup` | GA | `unique` 1.23, `weak` + `AddCleanup` 1.24 | https://go.dev/doc/go1.24 , https://go.dev/doc/go1.25 |
| `hash.Cloner` implemented by all stdlib hashes | GA | 1.25 | https://go.dev/doc/go1.25 |
| `uuid` package (RFC 9562; top-level import path `uuid`, no domain) | New | 1.27 | https://go.dev/doc/go1.27 , `go doc uuid` (section 9.1) |
| Generic methods (method declarations with own type parameters) | New | 1.27 | https://go.dev/doc/go1.27 |
| `new(expr)` | GA | 1.26 | https://go.dev/doc/go1.26 |
| `go vet` `stdversion` check run by `go test` by default | New | 1.27 | https://go.dev/doc/go1.27 |

Other 1.27 facts that touch this project:

- `asynctimerchan` GODEBUG removed; timer channels are always synchronous (unbuffered). https://go.dev/doc/go1.27
- `go mod tidy` for `go 1.27` modules merges duplicate `require` blocks into at most two (direct, indirect). https://go.dev/doc/go1.27
- `go doc` accepts `package@version` and `-ex` for examples. https://go.dev/doc/go1.27
- `net/http` `Server.MaxHeaderValueCount` is new (present in `go doc net/http.Server`). https://go.dev/doc/go1.27
- The only new vet analyzer behaviour in 1.27 is `printf` flagging `fmt.Errorf("...%w", p)` where `p` is `*E` and `E` implements `error`. https://go.dev/doc/go1.27

## 1. testing/synctest

API in 1.27: `synctest.Test(t, func(t *testing.T))`, `synctest.Wait()`, `synctest.Sleep(d)`. https://pkg.go.dev/testing/synctest

- `Sleep(d)` is exactly `time.Sleep(d); synctest.Wait()`. Use it in the test goroutine so the system under test "settles" after the shared sleep instead of racing the test goroutine. `go doc testing/synctest.Sleep`
- Bubble clock starts at midnight UTC 2000-01-01 and advances only when every goroutine in the bubble is durably blocked. https://pkg.go.dev/testing/synctest
- Durably blocking: channel send/recv on channels created in the bubble, `select` with only bubble channels, `sync.Cond.Wait`, `sync.WaitGroup.Wait` (when `Add` was called in the bubble), `time.Sleep`. NOT durably blocking: `sync.Mutex`/`RWMutex` Lock, network I/O, syscalls. https://pkg.go.dev/testing/synctest
- Verified on go1.27.1: a goroutine blocked on `mu.Lock()` while another goroutine sleeps 1s then unlocks never lets the clock advance; the test hangs until the `go test` timeout. Consequence: any code that waits on a timer-driven event (SQS long polling, visibility expiry, batching window) must wait on channels or `sync.Cond`, never on a Mutex hand-off, if it is to be testable in a bubble.
- Verified on go1.27.1: `httptest.NewTestServer(t, handler)` inside a bubble gives an in-memory network. A handler that `time.Sleep(30*time.Second)` (an SQS long poll stand-in) returned to a `srv.Client().Get` with `time.Since(start) == 30s` and ~0 real time. A loopback `httptest.NewServer` is not usable in a bubble because blocked socket I/O is not durable blocking. https://pkg.go.dev/testing/synctest (docs say this explicitly: "We cannot use a loopback network connection for this test").
- Timers, tickers and channels are bubble-scoped; touching a bubbled channel/timer from outside panics. `runtime.AddCleanup` and finalizers run outside the bubble. https://pkg.go.dev/testing/synctest
- `time.Now()` inside a bubble is the fake clock, so an emulator clock that wraps `time.Now()` plus an offset/scale is automatically virtualized under synctest; a clock that stores its own `time.Time` must be fed from the bubble clock in tests.
- The rule "use `testing/synctest` or the emulator clock for time" is satisfiable: use synctest for goroutine/timer scheduling tests of the engine; use the emulator clock (injected interface) where the API itself (admin `clock` freeze/advance/scale) is what is under test.
- Docker unix-socket I/O and `os/exec` are outside the bubble. Anything that touches Lambda containers cannot be bubble-tested.
- pgregory.net/rapid v1.3.0 has `rapid.SyncTest(t *rapid.T, prop func(*rapid.T))` which runs a property inside a bubble and still shrinks (section 12). Verified with `go test -race` on go1.27.1.

## 2. encoding/json/v2 and jsontext

### Status

- Release notes 1.27: `encoding/json/v2` and `encoding/json/jsontext` are shipped; v1 `encoding/json` is "now backed by the v2 implementation"; opt out with `GOEXPERIMENT=nojsonv2`, "expected to be removed in a future release". https://go.dev/doc/go1.27
- Locally: `go build` of a module importing `encoding/json/v2` succeeds with no flags; with `GOEXPERIMENT=nojsonv2` it fails with `build constraints exclude all Go files in .../encoding/json/v2`. The package files carry `//go:build goexperiment.jsonv2`. `$GOROOT/src/encoding/json/v2/arshal.go:5`, `$GOROOT/src/internal/buildcfg/exp.go:87`.
- Removed/renamed during the experiment (notes 1.27): tag options `format` and `unknown` removed, option `DiscardUnknownMembers` removed, `SkipFunc` removed, `inline` renamed `embed`; `jsontext.Token` numeric accessors now return errors. https://go.dev/doc/go1.27
- Whether the v2 API is covered by the Go 1 compatibility promise is not stated on the release-notes page. UNVERIFIED. Treat as stable-in-practice, but isolate it behind our own codec package so a break is a one-file fix.

### Behaviour verified on go1.27.1 (differences that matter for AWS JSON protocols)

| Input / Go value | v2 | v1 |
|---|---|---|
| nil slice | `[]` | `null` |
| nil map | `{}` | `null` |
| `[]byte` | std base64 string (`"aGkh"`) | same |
| `time.Time` | RFC 3339 string; unmarshal of a JSON number into `time.Time` errors | RFC 3339 string (number case not tested) |
| `time.Duration` | marshal error "no default representation" | integer nanoseconds |
| `<`, `>`, `&`, U+2028 in strings | emitted raw | escaped as `<` ... |
| duplicate object names | error `duplicate object member name "a"` | accepted |
| invalid UTF-8 in a string | error | replaced by U+FFFD |
| member name case (`{"A":7}` into field `a`) | no match, ignored | case-insensitive match |
| unknown members | ignored (set `json.RejectUnknownMembers(true)` to reject) | ignored |
| `1.0`, `1e2`, `1.5` into an `int` field | error (`invalid syntax`) | not tested |
| `[]byte` from unpadded base64 (`"aGk"`) | error `illegal base64 data at input byte 0` | same error; AWS blobs must be padded std base64 or need a custom unmarshaler |
| trailing data after top-level value | error | error |
| `null` into slice/pointer | leaves nil | same |
| map key order | random unless `json.Deterministic(true)`; with it keys are sorted | sorted |
| `any` holding big integer | `float64` (`1.2345678901234567e+19`) | same |

Sources: `go doc -all encoding/json/v2` (security section), https://pkg.go.dev/encoding/json/v2 , plus execution on this machine.

Consequences for the generator and codecs:

- AWS JSON timestamps are epoch-seconds numbers (possibly fractional) and durations/blobs have protocol-specific forms, so protocol codecs need `json.WithMarshalers`/`json.WithUnmarshalers` (`json.MarshalToFunc`, `json.UnmarshalFromFunc`) or a token-level decoder; do not rely on v2 defaults for `time.Time`. https://pkg.go.dev/encoding/json/v2
- Duplicate names and invalid UTF-8 are rejected by default. Real AWS services accept whatever their parser accepts; for client-sent bodies from the official SDKs this is not a practical risk, but a conformance case that sends invalid UTF-8 must be decided deliberately. UNVERIFIED what AWS does for these.
- Integer fields: `1.0`/`1e2` are rejected. Python `json.dumps(1.0)` would emit `1.0`; boto3's parameter validation rejects floats for integer shapes before serialization, so this should not occur from boto3 (UNVERIFIED; check against the protocol compliance suite which has number cases).
- For snapshots ("versioned, deterministic file"): `json.Deterministic(true)` sorts map keys; `jsontext.Value.Canonicalize()` implements RFC 8785 (verified: `{ "x" : [1, 2] }` becomes `{"x":[1,2]}`). https://pkg.go.dev/encoding/json/jsontext
- Struct tag options in the shipped version: `omitzero`, `omitempty`, `string`, `case:ignore|strict`, `embed`, `-`. `omitzero` uses `IsZero() bool` when present. https://pkg.go.dev/encoding/json/v2
- Streaming/token API for hand-written or generated decoders: `jsontext.NewDecoder(r, opts...)`, `PeekKind`, `ReadToken`, `ReadValue`, `SkipValue`, `StackPointer`; encoder `jsontext.NewEncoder`. `json.UnmarshalDecode`/`json.MarshalEncode` bridge the two. https://pkg.go.dev/encoding/json/jsontext
- Option set for tuning: `Deterministic`, `FormatNilMapAsNull`, `FormatNilSliceAsNull`, `MatchCaseInsensitiveNames`, `OmitZeroStructFields`, `RejectUnknownMembers`, `StringifyNumbers`, `jsontext.AllowDuplicateNames`, `AllowInvalidUTF8`, `EscapeForHTML`, `Multiline`, `WithIndent`. `go doc -all encoding/json/v2`, https://pkg.go.dev/encoding/json/v2
- Performance per release notes: "Marshal performance is broadly at parity ... unmarshal performance is significantly faster." https://go.dev/doc/go1.27

## 3. Iterators and helpers

- `iter.Seq[V]`, `iter.Seq2[K,V]`; `maps.Keys/Values/All/Collect/Insert`; `slices.All/Values/Backward/Chunk/Collect/Sorted/SortedFunc/SortedStableFunc/AppendSeq`. https://pkg.go.dev/iter , `go doc maps`, `go doc slices`
- `strings`/`bytes`: `Lines`, `SplitSeq`, `SplitAfterSeq`, `FieldsSeq`, `FieldsFuncSeq` (1.24). https://go.dev/doc/go1.24
- `reflect.Type.Fields/Methods/Ins/Outs` iterators (1.26). https://go.dev/doc/go1.26
- Deterministic generator output idiom: `for _, k := range slices.Sorted(maps.Keys(m))`. `go doc slices.Sorted`.
- `for range n` integer ranges and `sync.WaitGroup.Go` (1.25) are available and linted by `intrange`/`modernize`. https://go.dev/doc/go1.25
- Pitfall: yield after the iterator returned false panics; iterator over a mutex-guarded structure must not hold the lock across `yield` or it deadlocks with re-entrant callers. https://pkg.go.dev/iter
- `errors.AsType[E error](err)` (1.26) is the generic `errors.As`; prefer it for tagged error types. https://go.dev/doc/go1.26

## 4. Generic methods and `new(expr)`

- 1.27 permits methods with their own type parameters; interface methods may not. https://go.dev/doc/go1.27 . Relevant for typed registries (`func (r *Registry) Register[T any](...)`).
- `new(expr)` (1.26) removes helper `ptr(x)` functions for optional pointer fields in generated shapes. https://go.dev/doc/go1.26

## 5. net/http ServeMux, server behaviour

### Pattern grammar (1.22+)

`[METHOD ][HOST]/[PATH]`; wildcards `{name}`, `{name...}` (final segment only), `{$}`; `GET` also matches `HEAD`; most specific pattern wins, equal-specificity conflicts panic at registration; `Request.PathValue(name)`. Literal parts are case-sensitive. https://pkg.go.dev/net/http#ServeMux

### Verified hazards for an S3-style router (go1.27.1, handler `GET /` catch-all plus `PUT /{bucket}/{key...}`)

| Request | Result |
|---|---|
| `GET /bkt/a//b` | 307 to `/bkt/a/b` |
| `GET /bkt/a/../b` | 307 to `/bkt/b` |
| `GET /bkt/a/./b` | 307 to `/bkt/a/b` |
| `GET //bkt/x` | 307 to `/bkt/x` |
| `PUT /bkt` (only `/{bucket}/{key...}` registered) | 307 to `/bkt/` (CreateBucket would be redirected) |
| `PUT /bkt/a%2Fb/c` | match, `PathValue("key") == "a/b/c"` (the `%2F` is decoded; it is indistinguishable from `a/b/c`) |
| `GET /bkt/a%2F%2Fb` | handler sees `Path="/bkt/a//b"`, `RawPath="/bkt/a%2F%2Fb"` |
| `GET /bkt?uploads` | matches by path only; subresources in the query are invisible to the mux |

- Registration rules verified on go1.27.1: a wildcard host (`{bucket}.s3.localhost/`) panics at registration (`host contains '{'`); `/{bucket}?versioning` panics (`bad wildcard segment`); `/{key...}/` panics (`{...} wildcard not at end`); duplicate wildcard names panic. `GET /{bucket}/` and `GET /{bucket}/{key...}` conflict (same request set) and panic, while `PUT` and method-less variants of the same path register fine. Panics at registration are good for a static route table but mean dynamic registration must go through a recover or a pre-check.
- Documented: ServeMux cleans `.`/`..`/repeated slashes and redirects, and unescapes per segment. https://pkg.go.dev/net/http#ServeMux (sections "Request sanitizing", "Trailing-slash redirection"). Trailing-slash redirects use 307 since 1.26. https://go.dev/doc/go1.26
- S3 keys may legally contain `//`, `.` and `..` segments, so ServeMux must not front the S3 data plane. Route with a hand-written router over `r.URL.EscapedPath()` (or `r.RequestURI`, which is "the unmodified request-target as sent", `go doc net/http.Request`). `EscapedPath` returns `RawPath` when it is a valid encoding of `Path`. `go doc net/url.URL.EscapedPath`
- Dispatch for AWS needs headers and query: `X-Amz-Target` (JSON 1.0/1.1), `Action` form/query parameter (Query), S3 subresource query keys (`?uploads`, `?acl`, `?tagging`, ...), and the Host header for virtual-hosted addressing. None of these are expressible in ServeMux patterns (host patterns match exact hosts only; there is no wildcard host). ServeMux is adequate for the admin API under a reserved prefix and plausibly for path-and-method-routed REST surfaces such as the Lambda API and function URL paths (UNVERIFIED: check that no Lambda path segment, for example a function ARN, needs `//`, `..` or a literal `%2F`).
- Verified on the wire (raw TCP): a handler doing `w.Header()["x-amz-meta-foo"] = []string{"bar"}` sends `x-amz-meta-foo: bar` verbatim, while `w.Header().Set("x-amz-request-id", "abc")` sends `X-Amz-Request-Id: abc`. Use map assignment when exact lowercase is wanted (S3 returns lowercase `x-amz-meta-*`, UNVERIFIED against a real response). Incoming request header keys are canonicalized by the server (a request sent as `X-AMZ-Checksum-Crc32` was readable via `Header.Get("x-amz-checksum-crc32")` on the server side); tests that build `http.Request` values by raw map assignment with non-canonical keys will not be found by `Header.Get` (verified). The SDK itself writes `X-Amz-Checksum-Crc64nvme` etc. https://github.com/aws/aws-sdk-go-v2/blob/main/service/s3/serializers.go (`awsRestxml_serializeOpHttpBindingsPutObjectInput`)

### Server behaviours verified on go1.27.1 (raw TCP client)

- `Expect: 100-continue`: Go sends `HTTP/1.1 100 Continue` only when the handler first reads `r.Body`. A handler that writes 403 without reading the body gets `403 ... Connection: close` and no 100. That is the behaviour S3 clients rely on for early rejection.
- Limits: `Server.MaxHeaderBytes` (default `DefaultMaxHeaderBytes`), `Server.MaxHeaderValueCount` (new in 1.27), `http.MaxBytesReader(w, r.Body, n)` returns `*http.MaxBytesError`. `go doc net/http.Server`, `go doc net/http.MaxBytesReader`. understudy requires a body cap; wrap `r.Body` per-service with limits taken from each service's quota page (see the per-service research files), not a single global number.
- `http.NewResponseController(w)` gives `Flush`, `SetReadDeadline`, `SetWriteDeadline`, `EnableFullDuplex`, `Hijack`; needed for streaming responses (Lambda response streaming, S3 GetObject large bodies) and to disable the server write deadline on long polls. `go doc net/http.ResponseController`
- `Server.Protocols` can enable unencrypted HTTP/2 (h2c prior knowledge) alongside HTTP/1 on one port (1.24). https://go.dev/doc/go1.24
- `Request.Trailer` and `Transfer-Encoding: chunked` are handled by the server transparently. `aws-chunked` is an application-level body encoding (Content-Encoding) layered inside the HTTP body and is not handled by net/http; the stream must be decoded by our code (the SDK's encoder is a reference: https://github.com/aws/aws-sdk-go-v2/blob/main/service/internal/checksum/aws_chunked_encoding.go).

## 6. log/slog

- Core API: `slog.New(handler)`, `slog.NewJSONHandler(w, &slog.HandlerOptions{Level, AddSource, ReplaceAttr})`, `LevelVar` for runtime level changes, `Logger.LogAttrs(ctx, level, msg, attrs...)`, `slog.Group`, `slog.GroupAttrs(name, attrs...)` (1.25), `slog.DiscardHandler` (1.24), `slog.NewMultiHandler(h...)` (1.26), `testing/slogtest.TestHandler` for custom handlers. https://pkg.go.dev/log/slog , https://pkg.go.dev/testing/slogtest , https://go.dev/doc/go1.26
- Secret redaction: implement `slog.LogValuer`. Verified output on go1.27.1:
  `{"level":"INFO","msg":"request","service":"sqs","creds":{"access_key_id":"AKIAEXAMPLE","secret_key":"[redacted]"},"took":1500000000,"req":{"op":"SendMessage"}}`
  (`LogValue()` on a `Secret` string type returned `slog.StringValue("[redacted]")`; a struct implementing `LogValuer` returned a group). Types carrying credentials should implement `LogValuer` so that logging them can never leak. https://pkg.go.dev/log/slog#LogValuer
- `slog.Duration` serializes in JSON as integer nanoseconds (1500000000 above); use `slog.String("took", d.String())` or a `ReplaceAttr` if human units are wanted.
- The `time` attribute comes from `time.Now()` in the logger, not from the emulator clock. Logs are therefore wall-clock even when the emulator clock is frozen. Journal entries carry emulator time separately. Remove the key in tests with `ReplaceAttr` returning `slog.Attr{}` for `slog.TimeKey` (verified).
- `slog.SetLogLoggerLevel` controls only the `log` package bridge. https://pkg.go.dev/log/slog#SetLogLoggerLevel
- Linting: `sloglint` (section 13) enforces attr-only calls, static messages and key naming.

## 7. Checksums and hashes

### 7.1 Algorithm table (what S3 accepts)

The Smithy S3 model at `aws/api-models-aws` `main` defines `com.amazonaws.s3#ChecksumAlgorithm` = `CRC32, CRC32C, SHA1, SHA256, CRC64NVME, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128` and `ChecksumType` = `COMPOSITE, FULL_OBJECT`. https://github.com/aws/api-models-aws/blob/main/models/s3/service/2006-03-01/s3-2006-03-01.json (read with `gh api`). The S3 user guide lists the same ten algorithms and notes CRC64NVME is the default algorithm. https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity.html , https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html

| Algorithm | Request header (PutObject) | Multipart full-object | Multipart composite | Go stdlib |
|---|---|---|---|---|
| CRC64NVME | `x-amz-checksum-crc64nvme` | yes | no | `hash/crc64` with reflected poly `0x9A6C9329AC4BC9B5` |
| CRC32 | `x-amz-checksum-crc32` | yes | yes | `hash/crc32` `IEEE` |
| CRC32C | `x-amz-checksum-crc32c` | yes | yes | `hash/crc32` `Castagnoli` |
| SHA1 | `x-amz-checksum-sha1` | no | yes | `crypto/sha1` |
| SHA256 | `x-amz-checksum-sha256` | no | yes | `crypto/sha256` |
| SHA512 | `x-amz-checksum-sha512` | no | yes | `crypto/sha512` |
| MD5 | `x-amz-checksum-md5` | no | yes | `crypto/md5` |
| XXHASH64 | `x-amz-checksum-xxhash64` | no | yes | none |
| XXHASH3 | `x-amz-checksum-xxhash3` | no | yes | none |
| XXHASH128 | `x-amz-checksum-xxhash128` | no | yes | none |

Other headers: `x-amz-sdk-checksum-algorithm` (algorithm selector, SDK-style), `x-amz-checksum-type` (response/`CreateMultipartUpload`), `x-amz-trailer` (declares a trailing checksum header for `aws-chunked`). Source: PutObject API page (header list) https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html (fetched and grepped: the page lists `x-amz-checksum-{crc32,crc32c,crc64nvme,sha1,sha256,sha512,md5,xxhash64,xxhash3,xxhash128}`, `x-amz-sdk-checksum-algorithm`, `x-amz-checksum-type`, `x-amz-trailer`); support matrix from https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html . Documented: if a checksum does not match the supplied one S3 fails the request with `BadDigest`; part numbers for composite checksums must be consecutive from 1 or S3 returns HTTP 500. Same page.

- The official Go SDK implements only CRC32, CRC32C, SHA1, SHA256, CRC64NVME and SHA512 (`supportedAlgorithms`), so MD5/XXHASH* arrive only as precomputed header values from other clients. https://github.com/aws/aws-sdk-go-v2/blob/main/service/internal/checksum/algorithms.go
- The wire form is base64 (standard, padded) of the big-endian digest bytes: `hash.Hash.Sum(nil)` for `hash.Hash32/64` is big-endian. CORRECTED (was UNVERIFIED) for XXHASH*: the AWS CRT that botocore, the Java v2 SDK and the C++ SDK wrap writes the digest big-endian with seed 0 (8 bytes for XXH64 and XXH3-64; 16 bytes, `high64` then `low64`, for XXH3-128), so the header value is the standard padded base64 of those bytes (https://github.com/awslabs/aws-checksums/blob/12cd22e81543b8547132762023076ab6afa25559/source/xxhash.c#L227-L252 ; vectors and the remaining live-S3 question in [s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md)). Phase 1 should implement them, not return "unsupported".

### 7.2 CRC64NVME

- Parameters (CRC-64/NVME, aka Rocksoft): width 64, normal polynomial `0xAD93D23594C93659`, reflected polynomial `0x9A6C9329AC4BC9B5`, init `0xFFFFFFFFFFFFFFFF`, refin true, refout true, xorout `0xFFFFFFFFFFFFFFFF`, check("123456789") = `0xAE8B14860A799888`. Sources: AWS common runtime description ("bit-reflected ... based on the non bit-reflected polynomial 0xad93d23594c93659", inverts input and output) via https://github.com/awslabs/aws-checksums/blob/main/include/aws/checksums/crc.h ; NVMe polynomial and Linux "rocksoft" model https://lists.infradead.org/pipermail/linux-nvme/2022-March/030265.html .
- Go: `hash/crc64` has only `ISO` and `ECMA` constants, but `crc64.MakeTable(poly)` accepts any reflected polynomial, and Go's CRC already uses init and xorout of all ones, so the table form is correct as-is. The AWS SDK does exactly this: `const crc64NVME = 0x9a6c_9329_ac4b_c9b5` ("inverted NVME polynomial as required by crc64.MakeTable"), `crc64.New(crc64.MakeTable(crc64NVME))`. https://github.com/aws/aws-sdk-go-v2/blob/main/service/internal/checksum/algorithms.go ; `go doc hash/crc64`.
- Verified on go1.27.1 (`crc64.MakeTable(0x9A6C9329AC4BC9B5)`): check value `0xAE8B14860A799888`; empty input gives `0`.
- Independent catalog confirmation: the RevEng CRC catalogue lists `CRC-64/NVME` as width=64, poly=0xad93d23594c93659, init=0xffffffffffffffff, refin=true, refout=true, xorout=0xffffffffffffffff, check=0xae8b14860a799888, residue=0xf310303b2b6f6e42, class "attested". Trap: the catalogue notes "The published Check value is incorrect" in the NVMe spec (NVM Command Set Specification 1.0d, figure 120), so do not take the check value from the spec text; use `0xae8b14860a799888`. https://reveng.sourceforge.io/crc-catalogue/17plus.htm . The Linux kernel generates the same reflected table from `CRC64_NVME_POLY 0x9A6C9329AC4BC9B5ULL`. https://github.com/torvalds/linux/blob/master/lib/crc/gen_crc64table.c
- Trailer/header encoding: the S3 user guide states the trailer value is "a base64 encoding of the big-endian checksum value", so `rosUhgp5mIg=` is the trailer for `123456789` under CRC64NVME. https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html
- `MakeTable` for a non-ISO/ECMA polynomial builds a fresh simple table (`makeTable`), not slicing-by-8. Build the table once (package-level `sync.OnceValue`) and reuse; `crc64.Update(crc, tab, p)` supports incremental use. `$GOROOT/src/hash/crc64/crc64.go:41-52`.
- CRC32C is `crc32.MakeTable(crc32.Castagnoli)` (`Castagnoli == 0x82f63b78`), hardware accelerated on amd64/arm64. `go doc hash/crc32`, `$GOROOT/src/hash/crc32/`.

### 7.3 Test vectors (verified on go1.27.1; base64 of big-endian digest)

| Algorithm | `""` | `"123456789"` | `"Hello, world!"` |
|---|---|---|---|
| CRC32 | `AAAAAA==` | `y/Q5Jg==` (0xCBF43926) | `6+bG5g==` |
| CRC32C | `AAAAAA==` | `4waSgw==` (0xE3069283) | `yKEG5Q==` |
| CRC64NVME | `AAAAAAAAAAA=` | `rosUhgp5mIg=` (0xAE8B14860A799888) | `n3hVaQAaPTQ=` |
| SHA1 | `2jmj7l5rSw0yVb/vlWAYkK/YBwk=` | `98O8HYCOBHMq32eZZczDTKeuNEE=` | `lDpwLQbzRZmu4fjajvn3KWAx1pk=` |
| SHA256 | `47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=` | `FeKw08M4keuw8e9gnsQZQgwg4yDOlMZfvIwzEkSOsiU=` | `MV9b23bQeMQ7isAGTkoBZGErH853yGk0W/yUx1iU7dM=` |
| MD5 | `1B2M2Y8AsgTpgAmY7PhCfg==` | `JfnnlDI7RTiF9RgfG2JNCw==` | `bNNVbesNpUvKBgtMOUeYOQ==` |

(SHA512 vectors were also generated; omitted for length. CRC32/CRC32C/CRC64 check values for "123456789" agree with the standard catalog values `0xCBF43926`, `0xE3069283`, `0xAE8B14860A799888`.)

### 7.4 Full-object checksum of a multipart upload needs CRC combination

Go's `hash/crc32` and `hash/crc64` have no combine function (`go doc hash/crc32 | rg -i combine` is empty). For `ChecksumType=FULL_OBJECT` on CompleteMultipartUpload the server can either re-hash the stored object (one extra read pass) or combine per-part CRCs. Combination for the three CRC algorithms (init and xorout all ones) is `crc(A||B) = mulmod(x^(8*len(B)), crc(A)) XOR crc(B)`. The implementation below follows Mark Adler's `multmodp` from zlib's `crc32.c` (https://github.com/madler/zlib/blob/master/crc32.c) generalized to width 32/64. It passed a rapid property test (random data split into two random parts, all three algorithms, compared with the direct checksum) with `go test -race` on go1.27.1:

```go
// Params describes a reflected CRC whose init and final XOR are all ones.
type Params struct {
	Width int    // 32 or 64
	Poly  uint64 // reflected polynomial
}

var (
	IEEE   = Params{32, 0xEDB88320}
	CRC32C = Params{32, 0x82F63B78}
	NVME   = Params{64, 0x9A6C9329AC4BC9B5}
)

func (p Params) mulMod(a, b uint64) uint64 {
	m := uint64(1) << (p.Width - 1)
	var r uint64
	for {
		if a&m != 0 {
			r ^= b
			if a&(m-1) == 0 {
				break
			}
		}
		m >>= 1
		if b&1 != 0 {
			b = (b >> 1) ^ p.Poly
		} else {
			b >>= 1
		}
	}
	return r
}

// shiftOperator returns x^(8n) mod poly: appends n zero bytes.
func (p Params) shiftOperator(n uint64) uint64 {
	acc := uint64(1) << (p.Width - 1) // x^0
	sq := acc >> 1                    // x^1
	for range 3 {                     // x^8
		sq = p.mulMod(sq, sq)
	}
	for ; n != 0; n >>= 1 {
		if n&1 != 0 {
			acc = p.mulMod(acc, sq)
		}
		sq = p.mulMod(sq, sq)
	}
	return acc
}

// Combine returns CRC(A||B) given crcA, crcB and lenB = len(B).
func (p Params) Combine(crcA, crcB, lenB uint64) uint64 {
	if lenB == 0 {
		return crcA
	}
	return p.mulMod(p.shiftOperator(lenB), crcA) ^ crcB
}
```

Composite checksum (for SHA*, MD5, XXH*, and optionally CRC) is a hash over the concatenated raw part digests with a `-N` suffix; that format belongs to the S3 research file, not this one.

### 7.5 Other hash facts

- `hash.Cloner` (`Clone() (Cloner, error)`) is implemented by all stdlib hashes (unless `GOFIPS140=v1.0.0`), so a running checksum can be snapshotted at a part boundary without re-reading. https://go.dev/doc/go1.25 , `go doc hash.Cloner`
- MD5 and SHA-1 remain in the stdlib with no FIPS-related removal in 1.24-1.27 release notes. https://go.dev/doc/go1.24 (the notes only add `encoding.BinaryAppender` to both). Whether `GODEBUG=fips140=only` rejects them is UNVERIFIED (https://go.dev/doc/security/fips140).
- ETag for single-part objects is hex MD5; multipart ETag is `hex(md5(concat(md5(part_i))))-N` per the S3 guide. https://docs.aws.amazon.com/AmazonS3/latest/userguide/checking-object-integrity-upload.html
- SigV4: `crypto/hmac` + `crypto/sha256`, compare with `crypto/subtle.ConstantTimeCompare`. https://pkg.go.dev/crypto/hmac

## 8. archive/zip safety and os.Root

### 8.1 archive/zip

- `zip.NewReader`/`zip.OpenReader` return `ErrInsecurePath` (along with a usable reader) for non-local names or names containing a backslash only if the `zipinsecurepath=0` GODEBUG is set. The default in Go 1.20 through 1.27 is still `zipinsecurepath=1` (insecure paths accepted); `godebug.md` says "A future version of Go may change the defaults". `$GOROOT/src/archive/zip/reader.go:98-115,167-176`, `$GOROOT/doc/godebug.md:505-516`, https://pkg.go.dev/archive/zip#NewReader . Verified: default returns `err == nil` for a zip containing `../evil.txt`, `/abs.txt`, `a/../../evil2.txt`; `GODEBUG=zipinsecurepath=0` returns `zip: insecure file path`. A `//go:debug zipinsecurepath=0` directive in `main` makes it the default for the binary, but never rely on it alone.
- `zip.Reader.Open(name)` implements `fs.FS` semantics (no leading `/`, no `..`), and iterating `Reader.File` with `File.Name` is the unsafe path. `go doc archive/zip.Reader.Open`
- Reads are bounded by the header: the checksum reader returns `ErrFormat` once more than `UncompressedSize64` bytes are read and `io.ErrUnexpectedEOF` if fewer. `$GOROOT/src/archive/zip/reader.go:294-332`. The declared size itself is attacker-controlled, so enforce an explicit cap on the sum of `UncompressedSize64` before extracting (Lambda's deployment package limit is 250 MB unzipped, including layers: https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html ) and wrap each entry in `io.LimitReader`.
- Writing a symlink entry: `File.Mode()&fs.ModeSymlink != 0` marks it; the entry body is the link target. Skip or validate; never `os.Symlink` it blindly.
- `zip.Writer.Create` happily writes names like `../evil.txt` (used to build test fixtures); the compliance corpus for zip-slip tests can be generated in-test. Verified.

### 8.2 os.Root (Go 1.24, extended 1.25)

- `os.OpenRoot(dir)` returns `*os.Root`; methods fail if any component resolves outside the root, including via symlinks. Method set on 1.27.1: `Chmod Chown Chtimes Close Create FS Lchown Link Lstat Mkdir MkdirAll Name Open OpenFile OpenRoot ReadFile Readlink Remove RemoveAll Rename Stat Symlink WriteFile`; plus `os.OpenInRoot(dir, name)`. `go doc os.Root`, https://go.dev/doc/go1.24 , https://go.dev/doc/go1.25
- Documented limits: does not prohibit crossing filesystem boundaries, bind mounts, `/proc`, or device files; `Chmod/Chown/Chtimes` have a symlink-swap race on Unix; on `js` TOCTOU-vulnerable; on plan9/js references a name not an fd. https://pkg.go.dev/os#Root
- Verified on go1.27.1 (Linux), root = temp dir:
  - `MkdirAll("..")`, `OpenFile("/abs")`, `WriteFile("../x")`, `Rename("a","../zzz")`: error `path escapes from parent`.
  - `root.Symlink("/etc","lnk")` and `root.Symlink("../..","lnk2")` both SUCCEED (creation is not validated); `root.Open("lnk2/passwd")` then fails with `path escapes from parent`. The escape is blocked at use, not at creation.
  - `WriteFile("a//b")`, `"a/./b"`, `"a/../b"`: accepted syntactically (fail only because `a/` did not exist), so these keys alias `a/b`. `WriteFile("..")` fails; `WriteFile(".")` fails with "is a directory"; empty path fails; NUL byte fails with `invalid argument`; a 300-byte component fails with `file name too long`; a name with a backslash (`a\b`) is a legal single file name on Linux.
- `filepath.IsLocal(p)` is lexical only; `filepath.Localize` converts slash paths. `go doc path/filepath.IsLocal`
- The requirement "Mapping S3 keys to disk must be path-traversal safe" is therefore not satisfied by `os.Root` alone: S3 keys can contain `..`, `.`, `//`, trailing `/`, NUL-adjacent bytes, up to 1024 bytes with components over 255, and differ only by case on case-insensitive filesystems. Never use the key as a filename. Store object data under content-derived or opaque generated names (for example a hex object ID under `os.Root`) and keep key-to-ID in the metadata index; `os.Root` is then defense in depth.
- Zip extraction pattern that was verified: iterate `Reader.File`; reject symlink entries; for each regular entry `root.MkdirAll(path.Dir(name))` then `root.OpenFile(name, O_CREATE|O_WRONLY|O_TRUNC, 0o644)` and `io.Copy` from `io.LimitReader(rc, UncompressedSize64)`. Every escaping name (`../evil.txt`, `/abs.txt`, `a/../../evil2.txt`) was refused by `os.Root` with `path escapes from parent` and nothing was written outside the root. Combine with an upfront `filepath.IsLocal` reject so the failure is a clear typed error rather than an OS error.

## 9. unique, weak, runtime.AddCleanup

- `unique.Make[T comparable](v) Handle[T]`: canonicalizes values; handles compare by pointer, so equality is O(1). 1.25 reclaims unused entries more eagerly. https://pkg.go.dev/unique , https://go.dev/doc/go1.25 . Candidate uses: account IDs, region names, operation names, ARNs repeated across millions of journal events.
- `weak.Make(ptr) weak.Pointer[T]`, `.Value()` returns nil after the object is collected. https://pkg.go.dev/weak , https://go.dev/doc/go1.24
- `runtime.AddCleanup(ptr, fn, arg)`: multiple cleanups per object, may attach to interior pointers, panics if `arg == ptr`, not guaranteed to run before exit, not run for zero-size types; preferred over `SetFinalizer`. `go doc runtime.AddCleanup`, https://go.dev/doc/go1.24
- None of these may be used for correctness of resource lifetimes (Docker containers, temp dirs): cleanup is best-effort. Label-based Docker cleanup remains required. Cleanups also run outside synctest bubbles. https://pkg.go.dev/testing/synctest

### 9.1 uuid (new in 1.27)

- API: `type UUID [16]byte`; `New()` (currently equal to `NewV4`), `NewV4()`, `NewV7()`, `Nil()`, `Max()`, `Parse(s) (UUID, error)`, `MustParse`, `String()`, `Compare`, `MarshalText/UnmarshalText/AppendText`. Import path is plain `"uuid"`. `go doc uuid`, https://go.dev/doc/go1.27 , source `$GOROOT/src/uuid/uuid.go`.
- Randomness comes from `crypto/rand` (`rand.Read` at `uuid.go:191` and `:260`), so `testing/cryptotest.SetGlobalRandom(t, seed)` makes `uuid.New()` deterministic: verified two calls after the same seed returned the identical `6ae6783f-4fbd-491b-aeb8-8b73a48ed247`. `SetGlobalRandom` cannot be used in parallel tests. `go doc testing/cryptotest.SetGlobalRandom`
- `NewV7` takes its timestamp from the `time` package, so inside a `synctest` bubble it follows the fake clock: verified two V7 values drawn one bubble-hour apart sorted in increasing order, with the bubble clock at `2000-01-01 01:00:00 UTC`.
- `Parse` accepts the braced form `{123e4567-e89b-12d3-a456-426614174000}` and the 32-hex form without dashes, and returns `invalid uuid` otherwise (verified). Do not use it to validate fields that require the canonical dashed form; check with `String()` round-trip.
- `uuid` passes `depguard`'s `$gostd` allow-list (verified with golangci-lint 2.13.2), so the stdlib-only rule needs no exception. Under the "seed makes fault-injection runs deterministic" requirement, note that `uuid.New()` is not seedable at runtime (only via the test hook above); IDs that must be reproducible across emulator runs (snapshots, journal replay) need an injected generator.

## 10. encoding/xml: limitations that matter

Verified on go1.27.1 with an S3-shaped struct unless noted.

- Marshal with a namespaced `XMLName` tag (`xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`) emits `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` with children in the default namespace, which is the shape S3 responses use.
- The same tag on a struct used for Unmarshal is strict: `<ListBucketResult xmlns="urn:other">` fails with `expected element <ListBucketResult> in name space http://s3.amazonaws.com/doc/2006-03-01/ but have urn:other`, and a document with no xmlns fails too. S3 accepts request bodies both with and without the namespace, so request-side structs must NOT carry the namespace in `XMLName` (a tag without a namespace matches any namespace).
- Namespace prefixes cannot be controlled on output. A namespaced attribute (`xml:"http://www.w3.org/2001/XMLSchema-instance type,attr"`) produced `xmlns:_XMLSchema-instance="..." _XMLSchema-instance:type="foo"`. Workaround that produced `<Grantee xmlns:xsi="..." xsi:type="CanonicalUser">`: declare `Xmlns string \`xml:"xmlns:xsi,attr"\`` and `Type string \`xml:"xsi:type,attr"\``. On decode, an `xsi:type` attribute is found by local name (`xml:"type,attr"`) or with the namespace URI form; both worked. Open upstream issues: https://github.com/golang/go/issues/9519 , https://github.com/golang/go/issues/11496 , https://github.com/golang/go/issues/48821 , https://github.com/golang/go/pull/70944 (all open at time of writing).
- Escaping on output: `"` `&#34;`, `'` `&#39;`, tab `&#x9;`, newline `&#xA;`, CR `&#xD;`, `& < >` as entities. Characters outside the XML 1.0 `Char` range (for example U+0001) are silently replaced with U+FFFD on marshal (`$GOROOT/src/encoding/xml/xml.go:1951`); verified `"a\x01b"` became `a�b`. This is lossy. S3 handles such keys by `encoding-type=url` in listings; our listing code must apply URL encoding when requested and otherwise reject or replace deliberately. https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html (parameter `encoding-type`).
- The AWS SDK's own XML writer copies the stdlib escape table and adds two entries, `&#x85;` (NEL) and `&#x2028;` (LS), so a conforming server should do the same for text content. https://github.com/aws/smithy-go/blob/main/encoding/xml/escape.go . Stdlib does not escape them (verified: U+0085 passed through raw).
- DOCTYPE with an internal entity is rejected on decode (`invalid character entity &x;`), so XXE/billion-laughs is not a concern; control characters in input fail with `illegal character code U+0001`. `xml.Decoder.Strict` defaults true.
- Boolean decode accepts `True` (verified: `<IsTruncated>True</IsTruncated>` decoded to true) because it uses `strconv.ParseBool`; AWS emits lowercase.
- Empty vs absent: a non-pointer string field without `omitempty` always emits `<B></B>`; use `omitempty` or pointers to omit. Verified.
- Nested paths `xml:"Contents>Key"` repeat the wrapper per slice only if the slice is of structs; for a `[]string` it groups under one `<Contents>` (verified: `<Contents><Key>a&amp;b</Key><Key>c&lt;d</Key></Contents>`), which is wrong for S3 `ListBucketResult` where each object is its own `<Contents>`. Use a struct slice with `xml:"Contents"`.
- `encoding/xml` has many other open issues about well-formedness of generated names (https://github.com/golang/go/issues/81881) and no v2 is shipped in 1.27 (no `encoding/xml/v2` directory in `$GOROOT/src/encoding/xml`).
- AWS's Go SDK maintains its own XML encoder/decoder in `smithy-go` for exactly these reasons (https://github.com/aws/smithy-go/tree/main/encoding/xml and `transport/http/protocol/internal/xml`). For understudy, a small, schema-driven XML writer that we own is the safer choice for response bodies; `encoding/xml` is acceptable for decoding request bodies.

## 11. Stdlib crypto available for KMS (local cryptography)

The Smithy KMS model enumerates `KeySpec`: `RSA_2048, RSA_3072, RSA_4096, ECC_NIST_P256, ECC_NIST_P384, ECC_NIST_P521, ECC_SECG_P256K1, SYMMETRIC_DEFAULT, HMAC_224, HMAC_256, HMAC_384, HMAC_512, SM2, ML_DSA_44, ML_DSA_65, ML_DSA_87, ECC_NIST_EDWARDS25519`; `EncryptionAlgorithmSpec`: `SYMMETRIC_DEFAULT, RSAES_OAEP_SHA_1, RSAES_OAEP_SHA_256, SM2PKE`; `DataKeySpec`: `AES_256, AES_128`. https://github.com/aws/api-models-aws/blob/main/models/kms/service/2014-11-01/kms-2014-11-01.json

Stdlib coverage (`go doc`): AES-256-GCM (`crypto/aes` + `cipher.NewGCM`), RSA OAEP/PSS/PKCS1v15 (`crypto/rsa`), ECDSA P-256/384/521 (`crypto/ecdsa`, `crypto/elliptic` provides P224/P256/P384/P521 only), Ed25519 (`crypto/ed25519`), HMAC (`crypto/hmac`), HKDF (`crypto/hkdf`: `Extract`, `Expand`, `Key`), and ML-DSA 44/65/87 via the new `crypto/mldsa` in 1.27. https://go.dev/doc/go1.27 , https://pkg.go.dev/crypto/mldsa . Not in the stdlib: `ECC_SECG_P256K1` (secp256k1) and `SM2`/`SM2DSA`/`SM2PKE`; those key specs would need to return an AWS-shaped "unsupported" error in phase 1.

For deterministic crypto tests, `testing/cryptotest.SetGlobalRandom` (1.26) replaces the global randomness source; ECDSA/RSA now ignore the `rand` parameter, so injecting a random reader no longer works. https://go.dev/doc/go1.26

## 12. pgregory.net/rapid

- Latest: v1.3.0 (module proxy time 2026-03-30; GitHub release 2026-04-30). Repository https://github.com/flyingmutant/rapid (canonical import path `pgregory.net/rapid`), `go 1.23` in its `go.mod`. License is MPL-2.0 (file-level copyleft; fine as a test-only dependency but record it in the ADR since the project is Apache-2.0). Release notes: `Make[T]` gains field/type overrides, `t.Output`. https://github.com/flyingmutant/rapid/releases/tag/v1.3.0
- Entry points: `rapid.Check(t, func(t *rapid.T))`, `rapid.MakeCheck`, `rapid.MakeFuzz` (for `testing.F.Fuzz`), `rapid.SyncTest(t, prop)` (go1.25+ build tag), `T.Repeat(map[string]func(*T))` and `rapid.StateMachineActions(sm)` for stateful tests (a type with `Check(*rapid.T)` plus action methods `Name(*rapid.T)`). https://pkg.go.dev/pgregory.net/rapid
- Generators: `Bool Byte Int* Uint* Float* Rune String StringN StringOf StringMatching(regexp) SliceOf SliceOfN SliceOfDistinct SliceOfBytesMatching MapOf MapOfN OneOf SampledFrom Just Ptr Permutation Custom Deferred Map Make[T]() MakeCustom[T](MakeConfig)`; methods `Draw(t, label)`, `Filter`, `Example(seed...)`, `AsAny`. `go doc -all pgregory.net/rapid` in the module cache. Use `rapid.Custom` for domain types (filter patterns, queue names); `rapid.StringMatching` for ARNs and names with Smithy `@pattern` regexes.
- Flags: `-rapid.checks` (default 100), `-rapid.steps` (default 30, average Repeat actions), `-rapid.seed`, `-rapid.v`, `-rapid.shrinktime` (default 30s), `-rapid.failfile`, `-rapid.nofailfile`, `-rapid.log`, `-rapid.debug`. Failures write `testdata/rapid/<TestName>/*.fail`, which should be committed as regression seeds. `engine.go:66-82`, `persist.go:58`.
- Verified on go1.27.1 with `go test -race`: (a) incremental CRC64NVME equals one-shot for random splits; (b) `rapid.SyncTest` inside `rapid.Check` ran `time.Sleep(d)` and observed exactly `d` elapsed for random `d` up to an hour; (c) a state-machine test via `StateMachineActions`. All passed.
- `rapid.T` does not expose `t.Parallel()`; wrap properties in ordinary subtests if parallelism is wanted. UNVERIFIED beyond reading the exported method list.
- The "fast-check" preference in the global instructions is for TypeScript; for Go the pre-approved library is rapid.

## 13. golangci-lint v2 configuration

### Format (installed: v2.13.2, built with go1.27.0)

- File names searched: `.golangci.yml|yaml|toml|json`; first line must be `version: "2"`. Top-level keys: `version, linters, formatters, issues, output, run, severity`. https://golangci-lint.run/docs/configuration/file/
- `linters.default`: `standard` (default; errcheck, govet, ineffassign, staticcheck, unused), `all`, `none`, `fast`. `linters.enable/disable/settings/exclusions`. `gosimple` and `stylecheck` are merged into `staticcheck`; `gci/gofmt/gofumpt/goimports` moved to `formatters`. `golangci-lint migrate` converts a v1 file. https://golangci-lint.run/docs/product/migration-guide/
- `linters.exclusions`: `generated` (`strict` default, `lax`, `disable`), `presets` (`comments`, `std-error-handling`, `common-false-positives`, `legacy`), `rules` (by `path`, `path-except`, `text`, `source`, `linters`), `paths`, `warn-unused`. Formatter exclusions default `generated: lax`. https://golangci-lint.run/docs/configuration/file/
- No default timeout in v2. `golangci-lint config verify` validates against the JSON schema. `golangci-lint help linters`, `golangci-lint help formatters`.
- Renamed in 2.13: `wsl` -> `wsl_v5`, `exhaustruct` -> `exhaustruct_v5`, `gomodguard` -> `gomodguard_v2` (old names listed as deprecated by `golangci-lint help linters`). gofumpt `extra-rules` is deprecated in favour of `extra.group-params` (warning printed by the binary).
- Per-module runs: golangci-lint analyzes one module per invocation, so the conformance suites outside the main module each need their own run (and may use their own config). `run.relative-path-mode: gomod` exists. https://golangci-lint.run/docs/configuration/file/
- Generated-file detection honours the standard header, so checked-in generated code with `// Code generated ... DO NOT EDIT.` is skipped by linters under `generated: strict` (verified: a `zz_generated.go` with a bad name, `init`, and a flaggable `fmt.Sprintf` produced no findings).
- `issues.uniq-by-line` defaults to true: only one finding per line is shown, which hides second-linter findings on the same line. Set `uniq-by-line: false` to see everything (verified: `time.Now` was hidden by a `revive` finding on the same line until disabled).

### Proposed strict config for the main module (verified: `config verify` passes; smoke run on a sample file triggered errcheck, errorlint, exhaustive, forbidigo, forcetypeassert, gochecknoglobals, gochecknoinits, gocritic, godox, govet, modernize, revive, sloglint, unused; depguard blocked a non-stdlib import and gomoddirectives blocked a local `replace`)

```yaml
version: "2"

run:
  go: "1.27"
  tests: true
  modules-download-mode: readonly

linters:
  default: none
  enable:
    - errcheck
    - govet
    - staticcheck
    - unused
    - ineffassign
    - bodyclose
    - noctx
    - contextcheck
    - durationcheck
    - errorlint
    - exhaustive
    - gochecksumtype
    - forcetypeassert
    - nilerr
    - nilnesserr
    - nilnil
    - makezero
    - reassign
    - copyloopvar
    - fatcontext
    - recvcheck
    - gosec
    - errchkjson
    - nosprintfhostport
    - canonicalheader
    - usestdlibvars
    - gocheckcompilerdirectives
    - bidichk
    - errname
    - sloglint
    - revive
    - gocritic
    - unparam
    - unconvert
    - predeclared
    - perfsprint
    - prealloc
    - intrange
    - modernize
    - misspell
    - dupword
    - godot
    - godox
    - nolintlint
    - gochecknoinits
    - gochecknoglobals
    - iface
    - funcorder
    - containedctx
    - thelper
    - tparallel
    - usetesting
    - testableexamples
    - depguard
    - forbidigo
    - gomoddirectives
    - gocognit
    - nestif
  settings:
    errcheck: { check-type-assertions: true, check-blank: true }
    govet:
      enable-all: true
      disable: [fieldalignment, shadow]
    exhaustive: { default-signifies-exhaustive: false }
    gochecksumtype: { default-signifies-exhaustive: false }
    godox: { keywords: [TODO, FIXME, HACK, XXX] }
    nolintlint: { require-explanation: true, require-specific: true }
    sloglint:
      no-mixed-args: true
      attr-only: true
      no-global: all
      context: scope
      static-msg: true
      key-naming-case: snake
    godot: { scope: toplevel, capital: false }
    gocognit: { min-complexity: 20 }
    nestif: { min-complexity: 5 }
    gocritic:
      enabled-tags: [diagnostic, style, performance]
      disabled-checks: [hugeParam, ptrToRefParam]
    revive:
      rules:
        - name: exported
        - name: package-comments
        - name: error-return
        - name: error-naming
        - name: error-strings
        - name: context-as-argument
        - name: context-keys-type
        - name: indent-error-flow
        - name: unexported-return
        - name: blank-imports
        - name: receiver-naming
        - name: var-naming
        - name: unused-parameter
        - name: useless-break
        - name: unreachable-code
        - name: redefines-builtin-id
    depguard:
      rules:
        main:
          list-mode: strict
          files: ["!$test"]
          allow: [$gostd, github.com/sustinbebustin/understudy]
        tests:
          list-mode: strict
          files: ["$test"]
          allow: [$gostd, github.com/sustinbebustin/understudy, pgregory.net/rapid]
    forbidigo:
      analyze-types: true
      forbid:
        - pattern: ^fmt\.Print(f|ln)?$
          msg: use log/slog; stdout belongs to the CLI layer
        - pattern: ^time\.(Now|Since|Until|Sleep|After|AfterFunc|NewTimer|NewTicker|Tick)$
          msg: use the emulator clock (internal/clock) so time can be frozen and advanced
        - pattern: ^os\.Exit$
          msg: return an error to main
        - pattern: ^panic$
          msg: return an error value
    gomoddirectives: { replace-allow-list: [] }
  exclusions:
    generated: strict
    presets: [std-error-handling, common-false-positives]
    rules:
      - path: _test\.go
        linters: [gochecknoglobals, forbidigo, gocognit, nestif, forcetypeassert]
      - path: internal/clock/
        linters: [forbidigo]
      - path: cmd/
        linters: [forbidigo, gochecknoglobals]

formatters:
  enable: [gofumpt, goimports]
  settings:
    gofumpt: { extra: { group-params: true } }
    goimports: { local-prefixes: [github.com/sustinbebustin/understudy] }

issues:
  max-issues-per-linter: 0
  max-same-issues: 0
  uniq-by-line: false
```

Notes on the set:

- `depguard` with `$gostd` turns the "main module depends on the standard library only; test-only rapid" rule into a lint failure. Verified: importing `example.com/fake` from non-test code yields `import 'example.com/fake' is not allowed from list 'main' (depguard)`. The conformance suites are separate modules and get their own config. (`$test` and `$gostd` are depguard variables.) https://golangci-lint.run/docs/linters/configuration/
- `forbidigo` with `analyze-types: true` bans direct `time.Now`/`Sleep`/timers outside `internal/clock`, which is the lint-level enforcement of the controllable-clock feature. Verified: `time.Now` in a non-clock package was reported with the custom message. Under synctest, tests may need `time.Sleep`; the `_test.go` exclusion covers that. The ban on `panic` and `os.Exit` outside `cmd/` supports errors-as-values; `panic` for impossible states then needs `//nolint:forbidigo // reason`, which `nolintlint` forces to be specific and explained.
- `gochecksumtype` and `exhaustive` support the "illegal states unrepresentable / ADTs" rule: `exhaustive` handles enum-style `iota` types (verified: missing case flagged), `gochecksumtype` handles sealed interfaces annotated `//sumtype:decl`. https://github.com/alecthomas/go-check-sumtype
- `godox` keeps stray TODOs out; `godot` enforces sentence-final periods on top-level comments; `revive` `exported` plus `package-comments` enforce doc comments on exported API (verified: eight findings on an undocumented sample).
- Linters left out deliberately as noisy for this codebase: `wrapcheck`, `varnamelen`, `mnd`, `exhaustruct_v5`, `lll`, `wsl_v5`, `nlreturn`, `testpackage`, `paralleltest` (conflicts with synctest and rapid usage), `ireturn`, `err113` (flags dynamically created errors; revisit once the typed-error convention exists), `cyclop/gocyclo/funlen` (`gocognit` + `nestif` cover complexity).
- Formatters: `golangci-lint fmt` applies `gofumpt` and `goimports`; with Go 1.27's gofmt column-alignment change, "running gofmt from Go 1.27 on previously formatted code may produce minor whitespace changes". https://go.dev/doc/go1.27

## 14. How aws-sdk-go-v2 structures generated serialization

Two generations coexist in the same repo; check which one a service uses before copying a pattern.

### 14.1 Legacy per-operation middleware serializers (still used by `service/s3`)

- `service/s3/serializers.go` (about 500 KB) holds one middleware type per operation, for example `type awsRestxml_serializeOpPutObject struct`, with `HandleSerialize(ctx, in middleware.SerializeInput, next middleware.SerializeHandler)`; header/URI/query bindings live in `awsRestxml_serializeOpHttpBindingsPutObjectInput(v *PutObjectInput, encoder *httpbinding.Encoder) error`, which emits one `if v.X != nil { locationName := "X-Amz-..."; encoder.SetHeader(locationName).String(*v.X) }` block per member. Body documents use `awsRestxml_serializeDocument*` helpers. Deserializers mirror this (`deserializers.go`), with error mapping by error code string. https://github.com/aws/aws-sdk-go-v2/blob/main/service/s3/serializers.go
- Input validation (required members, range) is generated separately in `validators.go`, one `validateOp<Name>` per operation. https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/validators.go

### 14.2 Schema-based serde (used by newer generated clients, for example `service/sqs`, `service/lambda`)

Observed in the repo on 2026-10-08 (aws-sdk-go-v2 release 2026-10-06, smithy-go release 2026-10-07; both read through `gh api`):

- `service/sqs/` has no `serializers.go`/`deserializers.go`. It has `schemas/schemas.go` (65 KB), `common_serde.go`, `type_registry.go` and `types/`. Every file starts with `// Code generated by smithy-go-codegen DO NOT EDIT.` https://github.com/aws/aws-sdk-go-v2/tree/main/service/sqs
- `schemas/schemas.go` declares one `*smithy.Schema` var per shape and member, populated in a single `init`: `var SendMessageRequest = smithy.NewSchema(smithy.ShapeID{Namespace: "com.amazonaws.sqs", Name: "SendMessageRequest"}, smithy.ShapeTypeStructure, 7)` and `SendMessageRequest_QueueUrl = SendMessageRequest.AddMember("QueueUrl", _String)`, with Smithy traits attached as values, for example `&smithytraits.XMLName{Name: "MessageAttribute"}, &smithytraits.XMLFlattened{}`. https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/schemas/schemas.go
- Each input/output type implements `Serialize(s smithy.ShapeSerializer)`, `SerializeMembers(s)` and `Deserialize(d smithy.ShapeDeserializer) error`, both directions on both inputs and outputs (so the same generated types could serve a server): `s.WriteStruct(schemas.SendMessageRequest); v.SerializeMembers(s); s.CloseStruct()`, members written with `s.WriteString(schemas.SendMessageRequest_QueueUrl, *v.QueueUrl)`; collections via helper funcs in `common_serde.go` (`s.WriteList(schema); for ... s.WriteString(schema.ListMember(), ...); s.CloseList()`). https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/api_op_SendMessage.go , https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/common_serde.go
- `type_registry.go` maps `"com.amazonaws.sqs#BatchRequestTooLong"` to `{Schema, New: func() any { return &types.BatchRequestTooLong{} }}`: error shapes are found by shape ID at runtime. https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/type_registry.go
- The runtime in `github.com/aws/smithy-go`: `schema.go` (`Schema`, `ShapeID`, `ShapeType`, `OperationSchema`, `ServiceSchema`, `SchemaTrait[T]`), `serde.go` (`ShapeSerializer` with `Write{Int8..Int64,Float32,Float64,Bool,String,BigInt,BigFloat,Blob,Time,Union,Document,Nil,Struct,List,Map}` + `Close*`; `ShapeDeserializer` with `Read*`, `ReadStruct/ReadStructMember`, `ReadList/ReadListItem`, `ReadMap/ReadMapKey`; `Serializable`, `Deserializable`, `DeserializableError`), `traits/` (HTTP, serde and index traits). https://github.com/aws/smithy-go/blob/main/schema.go , https://github.com/aws/smithy-go/blob/main/serde.go
- Protocols are codecs implementing the two interfaces, one package each under `transport/http/protocol/`: `awsjson` (1.0/1.1, with `awsQueryCompatible` detected via `smithy.SchemaTrait[*traits.AWSQueryCompatible]`), `awsquery`, `ec2query`, `restjson1`, `restxml`, `rpcv2` (CBOR), over shared internals `internal/{json,xml,query,cbor,httpbinding}`. https://github.com/aws/smithy-go/tree/main/transport/http/protocol
- The SDK vendors a copy of the stdlib JSON scanner (`internal/json/internal/stdlib`) and has its own XML encoder/decoder (`encoding/xml`) rather than using `encoding/json` or `encoding/xml`. https://github.com/aws/smithy-go/tree/main/transport/http/protocol/internal/json

### 14.3 Which phase 1 services use which generation (aws-sdk-go-v2 `main`, read with `gh api repos/aws/aws-sdk-go-v2/contents/service/<name>` on 2026-10-08)

| Generation | Services |
|---|---|
| Schema-based (`schemas/`, `type_registry.go`, no `serializers.go`) | sqs, dynamodb, lambda, secretsmanager, kms, cloudwatchlogs, eventbridge, cloudwatch |
| Legacy (`serializers.go` + `deserializers.go`) | s3, sns, iam, sts |

The repo's root `CHANGELOG.md` (release 2026-10-06) repeats "Enable schema-based (de)serialization for this service" for dozens of services, so the legacy set is shrinking; the migration is in progress, not finished. https://github.com/aws/aws-sdk-go-v2/blob/main/CHANGELOG.md . Consequence: for the Query-protocol services (sns, iam, sts) and restXml S3, the reference to read is still the legacy generated code (`awsQuery_*`, `awsRestxml_*` helpers) plus `smithy-go` `transport/http/protocol`; for JSON and the other services read the schema-based output. `service/s3/go.mod` declares `go 1.24`, which is the SDK's minimum, not ours.

### 14.4 Request URIs carry an operation marker (S3)

The SDK's S3 serializers set the URI from the Smithy `@http` trait verbatim, including a fixed `x-id` query parameter naming the operation: `httpbinding.SplitURI("/{Key+}?x-id=PutObject")`, `"/{Key+}?x-id=CopyObject"`, `"/?analytics&x-id=GetBucketAnalyticsConfiguration"`. https://github.com/aws/aws-sdk-go-v2/blob/main/service/s3/serializers.go . Because it comes from the Smithy model's `uri`, the same marker should appear in requests from other SDKs generated from the same model. UNVERIFIED for boto3, JS v3 and the AWS CLI (not captured on the wire here). Real S3 ignores `x-id`; an emulator can use it as a cheap dispatch hint but must not require it, since hand-written REST clients and presigned URLs omit it.

Takeaways for the understudy generator are in section 17.

## 15. go generate and checked-in generated code

- Directive: `//go:generate command args...` at column 0, no space after `//`; the line is not parsed as Go, so it is also matched inside comments and raw strings. Variables: `$GOFILE $GOLINE $GOPACKAGE $GOOS $GOARCH $GOROOT $DOLLAR`; `//go:generate -command name cmd args` defines an alias for the rest of the file; the generator runs in the package directory; `go generate` sets the build tag `generate`; flags `-run`, `-skip`, `-n`, `-v`, `-x`. A failing generator stops processing that package. `go help generate`
- Never run automatically by `go build`/`go test`. `go help generate`
- Generated-file marker, verbatim from `go help generate`: a line matching `^// Code generated .* DO NOT EDIT\.$` which "must appear before the first non-comment, non-blank text in the file". `//go:build` constraints go after the header comment and a blank line (stdlib example `$GOROOT/src/internal/goexperiment/exp_jsonv2_on.go`: header, blank, `//go:build goexperiment.jsonv2`, blank, `package`). Origin: https://github.com/golang/go/issues/13560
- Stdlib pattern for generators: a `package main` file with `//go:build ignore`, referenced from the package that owns the output with `//go:generate go run gen.go` (for example `//go:generate go run mkconsts.go` in `$GOROOT/src/internal/goexperiment/flags.go:45`; `//go:generate go run gen_const_ppc64le.go` in `$GOROOT/src/hash/crc32/gen.go:5`). For a stdlib-only module a generator at `internal/cmd/gen` run with `go run ./internal/cmd/gen` needs no `tool` directive and no extra dependency. (`go.mod` `tool` directives exist since 1.24 for third-party tools: https://go.dev/doc/go1.24 .)
- Reproducibility rules for checked-in output (all idioms available in the stdlib): iterate maps via `slices.Sorted(maps.Keys(m))`; no timestamps, hostnames or absolute paths in the header; embed the pinned input identity (Smithy models commit) in the header or a lock file; format with `go/format.Source` so output is gofmt-clean; write through a temp file + rename so partial output never replaces a good file; make the generator fail (non-zero) on any model construct it does not understand rather than skipping it.
- CI drift check: `go generate ./... && git diff --exit-code` (and `git status --porcelain` for new files). golangci-lint ignores generated files by default, so the check is the only quality gate for them; add generated-code unit tests at the schema/codec level instead. https://golangci-lint.run/docs/configuration/file/
- Generation must be "reproducible with one command": expose it as a single `just generate` that runs `go generate ./...`; keep `//go:generate` directives next to the packages they feed so `go generate ./...` is the whole truth.

## 16. Verified-facts quick reference

- go1.27.1 on linux/amd64, `GOEXPERIMENT` empty, jsonv2 default-on, `GOROOT=/home/linuxbrew/.linuxbrew/Cellar/go/1.27.1/libexec`.
- `go doc` works offline for stdlib; module docs require the module in the module cache (`go doc pgregory.net/rapid` from inside the module dir worked).
- `pgregory.net/rapid` tests run under `-race` on 1.27.1.

## 17. Implications for understudy

1. JSON: build the JSON protocol codecs on `encoding/json/v2` plus `jsontext`, not v1. It is on by default in the installed toolchain, supports token-level streaming, deterministic output (`Deterministic(true)`, RFC 8785 canonicalization) and strictness switches. Wrap it behind one internal codec package, because (a) v2 defaults differ from AWS (`[]`/`{}` for nil, no epoch timestamp support, no `Duration`), (b) `nojsonv2` opt-out and API stability are not guaranteed by the release notes (UNVERIFIED), so only that package depends on the experiment-era surface. Add rapid round-trip properties for every codec.
2. Snapshots: serialize with `json.Deterministic(true)` (or canonicalize with `jsontext`) and include a format version; compare in tests byte-for-byte.
3. Routing: do not use `http.ServeMux` for S3 (and not for the AWS dispatch layer). It redirects `//`, `.`, `..` and subtree roots with 307, loses `%2F` vs `/`, and cannot dispatch on query, header or wildcard host. Write a small router over `r.RequestURI`/`EscapedPath()` and the Host header. Keep ServeMux for the admin API prefix and Lambda function URLs where its semantics are acceptable.
4. S3 on disk: never map keys to filenames; use opaque object IDs under an `os.Root`. Use `os.Root` for every disk access plus `filepath.IsLocal` pre-checks. Treat `os.Root` symlink creation as unvalidated: do not create symlinks from user input at all.
5. Lambda zip extraction: reject symlink entries and any non-`IsLocal` name up front (typed error), cap total declared uncompressed size and per-entry bytes (`io.LimitReader`), write through `os.Root`. Set `//go:debug zipinsecurepath=0` in the binary as a second layer. Zip-slip fixtures can be built in-test with `zip.Writer.Create("../x")`.
6. Time: structure the engine so every wait is a channel/`sync.Cond`/timer wait, never a Mutex hand-off, so `synctest` bubbles advance. Test the engine's timing semantics (visibility timeout, delays, long polls, batching windows, retention) with `synctest.Test` + `synctest.Sleep`; test HTTP layers with `httptest.NewTestServer` in a bubble (30 s long poll in ~0 s verified). Use rapid's `SyncTest` for properties over time. Forbid direct `time.Now`/`Sleep` outside `internal/clock` with `forbidigo` so the emulator clock cannot be bypassed.
7. Checksums: implement CRC64NVME as `crc64.MakeTable(0x9A6C9329AC4BC9B5)` (verified against check value), CRC32/CRC32C from `hash/crc32`, SHA1/SHA256/SHA512/MD5 from stdlib, all through `hash.Hash` so the aws-chunked decoder can stream-verify. Add the CRC combine routine (section 7.4) for FULL_OBJECT multipart checksums, or re-hash on complete. XXHASH64/XXHASH3/XXHASH128 are in the Smithy S3 model and the S3 docs but have no stdlib implementation and no official Go SDK support; decide by ADR between (a) hand-porting XXH64 (small) and XXH3/XXH128 (large, needs the 192-byte default secret), (b) accept-and-store without verification, or (c) return AWS's error for unsupported values. Digest encoding is now settled (big-endian, seed 0; see 7.1 and [s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md), which also carries cross-checked test vectors), which favours (a).
8. Test vectors: seed unit tests with the table in 7.3 and the `"123456789"` CRC check values; rapid properties for incremental vs one-shot hashing and for combine.
9. XML: own the response writer (schema-driven, with the SDK's extended escape table including NEL and LS, namespace and `xsi:` handling, and explicit URL-encoding for `encoding-type=url`); never emit via `encoding/xml` where control characters could reach output, because they are silently replaced by U+FFFD. Use `encoding/xml` only to decode request bodies, with namespace-free `XMLName` tags so documents with and without `xmlns` both parse.
10. Generator architecture: model it on the SDK's schema-based design: a generated `schemas` package of runtime shape descriptors with traits, plus symmetric `Serialize`/`Deserialize` on every shape against `ShapeSerializer`/`ShapeDeserializer`-style interfaces, protocol codecs as separate hand-written packages, and a type registry keyed by shape ID for errors. The SDK's types serialize both inputs and outputs, which is what a server needs; a hand-written equivalent of the interface (a few dozen methods) stays stdlib-only. Alternative: interpret the schema at runtime with no per-shape generated serde code (smaller output, slower, easier to keep correct). Pick one by ADR; the SDK's legacy per-operation middleware style (S3) is not a good model for a server.
11. Generated code: `// Code generated by <tool>; DO NOT EDIT.` matching `^// Code generated .* DO NOT EDIT\.$` as line 1 (before `//go:build`), generator under `internal/cmd/gen` invoked from `//go:generate go run ./internal/cmd/gen ...`, deterministic (sorted map iteration, `go/format`, no timestamps), CI drift check with `git diff --exit-code`. One `just generate`.
12. Lint: adopt the section 13 config (verified against golangci-lint 2.13.2) and let `depguard` enforce stdlib-only for the main module and rapid-only for tests; give conformance modules their own run. Keep `uniq-by-line: false` so overlapping findings are visible. Expect to tune `revive`/`gocritic` once the first real packages land.
12a. IDs: the 1.27 `uuid` package is stdlib, `depguard`-clean and deterministic in tests via `cryptotest.SetGlobalRandom`, so no third-party UUID module is needed. It cannot be seeded at runtime, so inject an ID generator interface wherever reproducibility across emulator runs matters (fault-injection seed, snapshots, journal replay); `uuid.NewV7` follows the `synctest` bubble clock but not the emulator clock, so do not use it for anything that should respect a frozen emulator clock.
13. Logging: `slog` JSON handler on stderr, `LevelVar` for runtime level, `LogValuer` on every type that can hold credentials or secret values, a `ReplaceAttr` that drops `time` in tests. Logs use wall time while the journal uses emulator time; record that in the architecture doc. `sloglint` with `attr-only` and `static-msg` keeps call sites uniform.
14. Property tests: add `pgregory.net/rapid v1.3.0` as the only test dependency (MPL-2.0; note in the ADR). Commit `testdata/rapid/**` failfiles as regression seeds. Use `StateMachineActions` for stateful models (SQS queue model vs real implementation, DynamoDB TTL shim) and `StringMatching` with Smithy `@pattern` expressions to generate valid names.
15. KMS: stdlib covers AES-GCM, RSA, ECDSA P-256/384/521, Ed25519, HMAC, HKDF and (new in 1.27) ML-DSA; secp256k1 and SM2 are not available, so those key specs need an explicit AWS-shaped unsupported error.
16. Toolchain posture: pin `go 1.27` in `go.mod`, run `go test -race` and `go vet` (the `stdversion` check now runs under `go test`), prefer `errors.AsType`, `new(expr)`, `for range n`, `sync.WaitGroup.Go`, iterators and `slices.Sorted(maps.Keys(m))` for deterministic output; `modernize` and `go fix` will flag older idioms.

## 18. Open questions

- Does AWS accept or reject JSON bodies with duplicate member names, invalid UTF-8 and `1.0` for integer members? v2 defaults reject all three; the Smithy protocol compliance tests are the place to check. Related files: [smithy-protocol-tests.md](smithy-protocol-tests.md).
- RESOLVED: XXHASH* digest wire format (see 7.1; the SDKs that send them are botocore, Java v2 and C++, all via the CRT). Still open: S3's behaviour when an unknown algorithm header arrives (record-backlog item R08).
- Is `encoding/json/v2` covered by the Go 1 compatibility promise in 1.27, and when is `GOEXPERIMENT=nojsonv2` removed?
- Whether `GODEBUG=fips140=only` affects MD5/SHA-1 (relevant only if users run understudy under FIPS mode).
- `rapid` use under `t.Parallel()` was not exercised.
