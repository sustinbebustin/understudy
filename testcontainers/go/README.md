# understudy for Testcontainers for Go

Package `understudy` runs [understudy](../../README.md), the local AWS emulator, as a [Testcontainers for Go](https://golang.testcontainers.org/) module.

A Go test that needs no container is better served by [`understudytest`](../../docs/guide/sdks.md#go-tests-with-understudytest), which runs understudy in the test process. Use this module when the suite already runs on testcontainers-go and needs understudy as a container, for example next to an application container on a Docker network.

The module is experimental until understudy 1.0 and is released with understudy: understudy `vX.Y.Z` is tagged `testcontainers/go/vX.Y.Z`, and `DefaultImage` names `ghcr.io/sustinbebustin/understudy:X.Y.Z`. Nothing is published yet, so build the image with `just image` and install the module from a clone, here at `../understudy`:

```sh
go mod edit \
  -replace github.com/sustinbebustin/understudy=../understudy \
  -replace github.com/sustinbebustin/understudy/testcontainers/go=../understudy/testcontainers/go
go get github.com/sustinbebustin/understudy@v0.0.0 github.com/sustinbebustin/understudy/testcontainers/go@v0.0.0
go mod tidy
```

It needs Go 1.27 and testcontainers-go v0.44.0.

## Use

```go
func TestOrders(t *testing.T) {
	ctx := t.Context()
	ctr, err := understudy.Run(ctx, "understudy:dev")
	if ctr != nil {
		testcontainers.CleanupContainer(t, ctr.Container)
	}
	if err != nil {
		t.Fatal(err)
	}
	env, err := ctr.Env(ctx) // AWS_ENDPOINT_URL, test credentials, us-east-1
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	// config.LoadDefaultConfig(ctx) now points every client at the container.
}
```

`Run` publishes port 4566 on `127.0.0.1` only and returns once `GET /_understudy/v1/health` answers 200 with `"ready": true`. It returns the container together with any error after the container was created, so register the cleanup before checking the error. `CleanupContainer` takes `ctr.Container`, because the module's `Endpoint(ctx)` replaces testcontainers-go's `Endpoint(ctx, proto)`. `Terminate` stops understudy with a 10-second grace period, which lets it remove the Lambda and DynamoDB Local containers it started, and every such container carries the session's labels, so Ryuk removes them too.

## Options

| Option | Effect |
|---|---|
| `WithDockerSocket()` | mounts the Docker socket and adds its group, for Lambda and DynamoDB Local; the start fails unless understudy then reports Docker enabled |
| `WithDockerSocketGroup(gid)` | the same, with the group given, for a daemon that is not a local unix socket |
| `WithClock(Running{})`, `WithClock(Frozen{At: t})` | the emulator clock |
| `WithClockScale(f)` | clock speed |
| `WithSeed(n)` | repeatable generated identifiers |
| `WithSigV4Verify()` | verify request signatures |
| `WithSnapshot(path)` | load a snapshot archive at start |
| `WithAdminToken()` | a random admin token, sent by the wait and the helpers |
| `WithPublishedOn(addr)` | publish on another address; beyond loopback only with `WithAdminToken()`; a loopback address other than `127.0.0.1` is refused, since the endpoint never dials it |

testcontainers-go's own customizers apply after these, so `testcontainers.WithEnv` sets any other `UNDERSTUDY_*` setting. `Admin(ctx)` returns an `admin.Client`; `Settle`, `AdvanceClock` and `Reset` wrap the common calls. Refusals before any container is created are `*ExposureError` and `*SocketGroupError`; a mounted socket understudy cannot use is `*DockerUnavailableError`.

The [Testcontainers guide](../../docs/guide/testcontainers-modules.md) covers Lambda, Terraform and cleanup in more detail.

## Development

```sh
just image
testcontainers/go/run.sh    # go vet, golangci-lint with .golangci.yml, sealed go test -race against understudy:dev
```

`UNDERSTUDY_IMAGE` selects another image; extra arguments go to `go test`.
