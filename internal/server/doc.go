// Package server runs understudy serve: the listeners, the explicit
// construction of every service in wire.go, startup with the persisted
// state, shutdown, the containers Lambda and DynamoDB Local run in, and the
// startup sweep of what a crashed run left.
//
// It is the only package that names every service, every codec and the
// outbox.
//
// Decisions the architecture leaves open:
//
//   - Config is what the CLI maps config.Config onto. The settings the CLI
//     may not import a service to express (the fidelity switches) are the
//     services' own types, re-exported as aliases with their constants;
//     settings that need parsing against the region table (hot-reload
//     bindings, extra regions) arrive as strings and are parsed in wire.go.
//     The CLI reads the environment, including DOCKER_HOST, and this
//     package reads none.
//   - A non-loopback listen address needs the ContainerPublished exposure,
//     which New accepts only inside a container with its own network
//     namespace, outside Kubernetes (config.DetectPlacement), or, on the
//     host, an admin token and AWSAPIUnauthenticated: the token guards only
//     the admin API, and every client may sign the AWS API with the public
//     dummy pair test/test, which verifies even under SigV4Verify, so
//     serving it beyond loopback is an explicit choice. Otherwise New
//     refuses with a *ListenError that explains the options, and the banner
//     warns about what an accepted one exposes. An IPv4 address listens on
//     IPv4 only, and a wildcard listener is advertised (ready file, banner)
//     at loopback.
//   - The admin API (internal/server/admin) serves the /_understudy/v1/
//     prefix; its health endpoint answers 503 {"ready": false, "waiting":
//     ["listener"]} until Serve's listeners accept. Health also reports
//     Docker (disabled without a socket, degraded when it does not answer a
//     ping) and DynamoDB Local (degraded with the last start's error until
//     a start succeeds); DynamoDB takes Docker's state and reason while
//     Docker is not enabled, and Lambda is degraded to its control plane,
//     with the reason, while Docker is not enabled or lambda.network is
//     disabled. Health names the main listener's bound address under
//     "listener" and the topology under "topology": the detected one once a
//     container needed it, before that in-container inside a container and
//     host otherwise (admin.Topology).
//   - lambda.network bridge skips detection with the configured gateway as
//     a Linux bridge; disabled keeps the pool but refuses every function
//     container with a *LambdaNetworkDisabledError, which Invoke reports
//     as ServiceException, while DynamoDB Local still runs.
//   - A configured snapshot (Config.LoadSnapshot) loads after the
//     persisted state, the way PUT /snapshots does, so a persisting run
//     checkpoints it as a change.
//   - The topology is detected, and the launcher, the Runtime API server
//     and the container listener built, at first use by a function
//     environment, an image pull or DynamoDB Local (containers.go), not at
//     startup: they need the main listener's port, which Serve learns. The
//     Lambda pool and execution layer are built in wire.go against the
//     containerHost, which stands in for all three until then, so the
//     admin hooks and snapshots see the execution layer from the start.
//     Without Docker configured, or without its socket, no pool is built:
//     Invoke answers 501 NotImplemented naming Docker and the control
//     planes keep working.
//   - The container listener serves the AWS API and, from the admin
//     prefix, only health and clock (edge.ListenerContainer); it opens on
//     the topology's address at the main port and closes with the others.
//     It accepts only peers inside the subnet of the host interface that
//     holds that address (the bridge network) and closes every other
//     connection with a warning: Linux accepts packets for a local address
//     on any interface, so a LAN peer routing the bridge subnet through
//     this host would otherwise reach the AWS API (peers.go).
//   - DynamoDB Local's manager is built at its first start, when the
//     topology says which network it joins (lazyLocal), and that first
//     start logs ddblocal.Notice, the licence it runs under. With DynamoDB
//     enabled and the pinned image already present it starts in the
//     background at serve; otherwise the first DynamoDB call starts it.
//     Under persistence it stores items under <dir>/dynamodb.
//   - Containers are owned per data directory: the owner label is a hash
//     of the directory's absolute path, so a restart on the same directory
//     sweeps what a crashed run left, under the directory's flock. Without
//     persistence the owner is the run's identity (runs.go): boot id, PID
//     namespace, PID, process start time and instance, which also names
//     its temporary directory (blob spill and extracted code). Every serve
//     removes, in the background at start, the containers, networks and
//     temporary directories of such runs whose process is gone; a run of
//     another boot or PID namespace is never judged gone. Where there is no
//     procfs the owner is random and `understudy cleanup` removes
//     leftovers.
//   - Persistence (persist.go) locks <dir>/.lock, keeps blobs in
//     <dir>/blobs and the archive at <dir>/state.snapshot, loaded in New.
//     A checkpoint is a snapshot cut whose sections and fault rules are
//     hashed; it is written only when that digest differs from the last
//     saved, since the clock, journal counter and id positions move without
//     any state changing, and always at shutdown. The blob store is synced
//     before the archive is renamed into place and the directory is synced
//     after, and the blobs the archive on disk names stay pinned until a
//     newer one has durably replaced it, so a crash after a delete loses
//     only the changes since the last checkpoint. At start a Running clock
//     resumes at the later of the saved instant and wall time, since wall
//     time passed while no serve ran; a Frozen or Scaled one resumes from
//     the saved instant. There is no write-ahead log (ADR 0009). When one
//     is scheduled, ADR 0016 (dependency policy amendment) pre-qualifies
//     bbolt for it, beside the archive and not instead of it, with its gate
//     re-run against the then-current version.
//     Extracted function code lives in <dir>/code, or in the run's
//     temporary directory without persistence.
//   - The TLS listener's certificate is generated at every start
//     (ECDSA P-256, its own CA, SANs localhost, *.localhost, 127.0.0.1,
//     with critical name constraints to localhost, names under it and
//     loopback addresses, so as a trust root it vouches for nothing else),
//     and its URL is the ready file's tlsEndpoint. The certificate is not
//     written to disk; a client trusts it by reading it from the handshake.
//   - The admin hooks (draining, pollers, hot reload, image pulls, the TTL
//     sweep, subscription tokens) are assigned by type, so the compiler
//     checks that each service implements its hook. The outbox's
//     PolicySource routes on the target ARN's service to SQS, SNS or
//     Lambda, which read the queue's Policy, the topic's Policy and the
//     function's permissions for the access advisory.
//   - Each service is named in apis(), its constructor block and its
//     collect call; New refuses a registered service that is not collected
//     with an *UncollectedServiceError, so none can silently drop out of
//     snapshots, persistence, resets and the resource listing.
//   - The kernel takes its Linker before the Lambda execution layer that
//     implements it exists, so wire.go hands it a deferredLinker that the
//     execution layer is set into.
//   - The snapshot runtime restores id stream positions only when both the
//     archive and this run are seeded, keeps this run's seed, and moves the
//     journal counter forward only, so an event id is never issued twice.
//   - The ready file is written atomically (temporary file and rename) once
//     the listeners accept, and removed at shutdown, so a stale file never
//     names a dead endpoint. New refuses a ready file whose directory it
//     cannot write with a *ReadyFileError, before anything listens.
//   - Shutdown cancels every request's context first, so parked long polls
//     end at once, gives requests in flight five seconds, stops the
//     background work (the pool's watch, checkpoints, the sweep and the
//     background DynamoDB Local start), removes the Lambda environments
//     (the pool drains them; function runtimes ignore SIGTERM) and DynamoDB
//     Local, which refuses any later start, stops the kernel, then saves
//     the state. With persistence, the startup sweep runs before the
//     background DynamoDB Local start, so a crashed run's container never
//     shares <dir>/dynamodb with the new one.
//   - The banner is a text block on the log output with text logs and one
//     record with JSON logs, so a log pipeline never sees an unparsable
//     line. It reads the components as health does: it lists every
//     service, marking those that cannot serve with their state, such as
//     "lambda (degraded: control plane only)", and warns when Docker is
//     missing or does not answer and when lambda.network is disabled.
//   - Codecs are adapted to edge.Codec here (codecs.go), since the edge may
//     not import a protocol package.
//   - Boot fails with an *UnhandledTimerError when the loaded state armed a
//     timer whose kind no handler is registered for: New checks after
//     loading, and Serve again once the pollers have registered their
//     kind.
//   - Serve reports readiness three ways: the ready file, Config.OnReady
//     with the endpoints (for in-process callers such as understudytest),
//     and the health endpoint. A listener it cannot open is a *BindError.
package server
