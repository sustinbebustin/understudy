// Package config defines the Config type with sum-typed modes, parses it from
// flags, UNDERSTUDY_* environment variables and an optional JSON file in that
// order of precedence, and validates it against the defaults (ARCHITECTURE.md
// 18, ADR 0017).
//
// Load is the one entry point. Every refusal is an *Error that names the
// setting as its source spells it (--listen, UNDERSTUDY_LISTEN,
// understudy.json: s3.crossRegion) and, for an unknown flag, variable or key,
// the closest valid name. Durations are Go duration strings and times RFC
// 3339.
//
// Decisions the architecture leaves open:
//
//   - Scalar settings have a flag, a variable and a file key, the flag name in
//     camelCase (Settings lists them); the structured settings (accounts,
//     regions, s3, lambda, sqs, iam, dynamodb, journal, faults) are file
//     sections only. The fidelity switches are keyed by their switch names
//     (s3.crossRegion, lambda.stateTransitions, sqs.queueDeletedRecently,
//     iam.assumeRole).
//   - An UNDERSTUDY_* variable no setting reads is refused, so a typo is not
//     ignored; UNDERSTUDY_ENDPOINT, the CLI's, is allowed. An empty variable is
//     unset.
//   - Where understudy runs decides the default listener: Load reads the
//     container markers internal/topology reads (/.dockerenv,
//     /run/.containerenv, /proc/1/cgroup) through its readFile, which tests
//     replace. Inside a container the default is Exposed{0.0.0.0:4566,
//     ContainerPublished}, as the image needs; ContainerPublished
//     (listenAuth container-published) is refused outside one. The server
//     checks the container again at boot.
//   - The admin token has no flag, which would show in the process list. A
//     token with a loopback listener is refused rather than ignored; beyond
//     loopback a token wins over container-published and must be at least
//     16 characters, since nothing rate-limits a peer's guesses.
//   - lambda.bridgeGateway, where the container listener binds and serves
//     the AWS API unchecked, must be a private (RFC 1918) IPv4 address, so
//     a typo or a public address cannot open that API beyond the machine.
//     A private LAN address still passes here: only the daemon's networks
//     can tell a bridge gateway from one, and the server checks those
//     before it opens the listener.
//   - --tls enables the self-signed listener at the main address on port
//     4567, or port 0 when the main port is 0, so parallel instances never
//     collide; --tls-listen sets the address and implies --tls.
//   - The DynamoDB licence is Disabled, AwaitingLicence or LicenceAccepted.
//     Accepted carries no digest: only internal/service/dynamodb/ddblocal
//     knows the pinned digest, and it records an acceptance per digest in the
//     data directory. UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE accepts only the
//     value 1 and refuses anything but 1 and 0.
//   - Docker is DockerSocket{Path} or DockerDisabled, from --docker
//     (unix://<socket> or disabled), else DOCKER_HOST, else the default
//     socket. An explicit setting that is not a unix socket is refused, since
//     the Docker client speaks only to one; a DOCKER_HOST that is not one
//     (tcp:// in docker-in-docker CI, ssh://) is Docker's setting rather than
//     understudy's, so it turns containers off and adds an entry to
//     Config.Warnings, which serve logs and doctor reports.
//   - LoadHost resolves only the machine settings (persistence, Docker,
//     hot-reload mappings, the licence) for the client commands doctor and
//     cleanup: UNDERSTUDY_ADMIN_TOKEN is also the CLI's own credential, and
//     serve's rule that a loopback listener takes no token must not refuse
//     them.
//   - Hot-reload mappings parse their account and region at load, the region
//     against AWS's table plus the configured extra regions; zero values are
//     000000000000 and us-east-1.
//   - S3 base domains in the file are added to localhost and s3.localhost and
//     must resolve to loopback by themselves: localhost, names under it, or
//     localhost.localstack.cloud for LocalStack migrations.
//   - ARCHITECTURE.md 18's Journal.Bodies is MaxBytes, the byte budget that
//     bounds the events kept and with them the call bodies recorded.
//   - Running takes no start time: neither the server nor the clock can start
//     a running clock anywhere but at wall time.
//   - The clock starts Running, Frozen{At} or Scaled{Factor}: --clock-scale
//     makes a running clock Scaled, and with a frozen --clock it is refused
//     naming both settings rather than kept for a later resume.
package config
