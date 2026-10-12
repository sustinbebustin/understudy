// Package cli implements the understudy subcommands as admin.Client calls plus
// output formatting (ARCHITECTURE.md 17, ADR 0017). It imports no service
// package, and it is the only package that writes to stdout.
//
// Every command takes --output text|json and --endpoint (else
// UNDERSTUDY_ENDPOINT, else http://127.0.0.1:4566), never prompts, and has
// examples in its --help; 'understudy help CMD' is 'understudy CMD --help'.
// Exit codes come from admin.ExitCode: 0 success, 1 an operation that
// failed, 2 usage, 3 understudy unreachable, 4 a condition not met. Errors
// go to stderr naming what failed and the command to try next; with
// --output json they are {"error": {"code", "message", "hint", "exit"}}.
// A failure of the CLI's own file I/O (a rule file, an archive, an overlay)
// carries its next step as the hint.
//
// Decisions the architecture leaves open:
//
//   - The admin token for a server beyond loopback is read from
//     UNDERSTUDY_ADMIN_TOKEN only; a flag would show in the process list.
//   - Flags may follow positional arguments (clock advance 15m --settle).
//   - serve parses its own flags through internal/config and maps the result
//     onto server.Config with serverConfig. A setting this build's server
//     does not apply yet (sigv4 verify, accounts.keys, s3.maxObjectBytes,
//     dynamodb.registry) is refused by name with exit 2 rather than ignored.
//     serve takes --output as another name for --log-format, its logs go to
//     stderr, and with JSON asked for, by flag or UNDERSTUDY_LOG_FORMAT, a
//     configuration error is the JSON envelope too. The configuration's
//     warnings are logged before the server starts, each with its setting
//     and fix. An address another process holds is a usage error (exit 2)
//     naming the address and serve --listen 127.0.0.1:0.
//   - journal --output json writes NDJSON, one event per line, for queries
//     and --follow alike. --follow subscribes to the stream first and waits
//     until the server has, then prints every matching event, without the
//     --limit a query has, then the stream's, skipping those the query
//     printed, so nothing recorded in between is lost; a canceled context
//     ends it with exit 0. --since is emulator time, read from the server's
//     clock.
//   - snapshot save writes through a temporary file and a rename. snapshot
//     load streams the archive to the server; inspect and diff stream local
//     archives through internal/snapshot, which stops at 4 GiB, and accept any
//     section version; diff exits 0 whether or not the archives differ, and
//     its JSON says which.
//   - faults load takes a JSON list of rules or the document 'faults list
//     --output json' prints, so a saved list loads back.
//   - coverage asks the server at the endpoint for its JSON report and, when
//     none answers, wires the emulator in process and asks that, so the two
//     agree byte for byte. --overlays adds the overlay evidence of a directory
//     such as codegen/overlays, which the binary does not carry.
//   - env prints AWS_ENDPOINT_URL, the test keys (account 000000000000) and
//     AWS_REGION, plus AWS_DEFAULT_REGION for boto3, the AWS CLI and
//     Terraform; the S3 path-style settings that have no variable are
//     comment notes (stderr for the github format, which holds assignments
//     only), with the server's DynamoDB licence line when it waits for one.
//     env --tls (ADR 0013) points AWS_ENDPOINT_URL at the TLS listener, by
//     default the endpoint's host at port 4567 as serve --tls binds it, reads
//     the certificate the listener presents, accepts it only when it is
//     self-signed and valid for that host, writes it as PEM (--ca-file,
//     default understudy/ca-<port>.pem in the user cache directory, not the
//     shared temporary one) and
//     prints AWS_CA_BUNDLE, plus NODE_EXTRA_CA_CERTS for JS, whose SDK does
//     not read AWS_CA_BUNDLE. The health endpoint does not name the TLS
//     listener, so a non-default one is passed with --tls-endpoint.
//   - doctor and cleanup read the machine settings with config.LoadHost, so
//     UNDERSTUDY_ADMIN_TOKEN, the CLI's own credential, does not fail them.
//   - doctor checks the server, the configuration and its warnings, Docker
//     (socket and permissions, through internal/docker), the topology
//     (internal/topology, with its remedy when unsupported), *.localhost
//     resolution, binfmt on Linux, the configured hot-reload directories
//     against Docker Desktop's default shared paths, and the DynamoDB Local
//     licence with its exact flag and variable; when the server reports
//     DynamoDB Local down because Docker is, the fix is Docker's. It exits 1
//     when a check fails; warnings do not fail it.
//   - cleanup removes the containers and empty networks labeled
//     understudy.dev/managed of one deployment, through docker.Client.Sweep:
//     the owner serve derives from the --persist directory (configured, or
//     cleanup --persist), which its JSON result names. It holds that
//     directory's lock while it sweeps, as Sweep requires, so it refuses
//     while a serve of the directory runs, on whatever port, and no serve of
//     it starts mid-sweep. A run without persistence labels its objects with
//     an owner drawn at start, which nothing else can derive, so cleanup then
//     asks for --all, which removes the objects of every understudy on the
//     daemon, running ones included, except those labeled
//     understudy.dev/test, which their harness removes; --all refuses while
//     an understudy answers at the endpoint.
package cli
