# ADR 0011: Container networking and listener exposure

Status: accepted, 2026-10-08.

## Context

The brief binds to 127.0.0.1 by default and requires SDK calls inside functions to route back to understudy. A listener on 127.0.0.1 is unreachable from a bridge-network container; `host-gateway` resolves to the bridge gateway IP (`containers.md` finding 4). Go v2 and JS v3 build virtual-hosted S3 URLs for any non-IP endpoint, and `*.localhost` resolves to the container's own loopback; only JS v3 routes SQS calls by `QueueUrl`; `--add-host localhost:host-gateway` makes `localhost:<port>` URLs reach the host for Go, boto3, Node and urllib (`lambda-runtime-contract.md 5 to 6`). Docker Desktop, rootless Docker and Podman differ (`containers.md 8.1`).

## Decision

- `internal/topology` detects the environment once at first Lambda use and returns a sealed `Topology` (`LinuxBridge{Gateway}`, `DockerDesktop`, `RootlessDocker`, `Podman{Rootless}`, `InContainer{Network, Self}`) that yields the bind address for the container listener and the Runtime API listeners, the IPv4-literal `AWS_ENDPOINT_URL` for functions, and the `ExtraHosts` to add.
- A container listener is created on demand on the topology's gateway address at the main port. It serves the AWS API (and only `GET /health` and `GET /clock` from the admin prefix), never `0.0.0.0`, and can be disabled (`lambda.network: disabled`, control plane only). Per-environment Runtime API listeners bind the same address on ephemeral ports.
- Functions get `AWS_ENDPOINT_URL` as an IPv4 literal (path-style in every SDK, no DNS), `AWS_REGION`, minted execution-role credentials and `AWS_EC2_METADATA_DISABLED=true`. On Linux and Podman, `ExtraHosts: ["localhost:host-gateway"]` is added so `localhost:<port>` URLs minted on the host work inside functions; never on Docker Desktop.
- Linux bridge is first-class and CI-tested; Docker Desktop and Podman are best effort with `understudy doctor` checks; rootless Docker is unsupported in phase 1 with a specific error.
- A missing first `GET /next` within the init limit produces an error naming the address handed to the container, the detected topology and the three likely causes (loopback-only listener, `docker0` firewall, rootless loopback block).

## Alternatives considered

- Bind the main listener on `0.0.0.0`: simplest, exposes an unauthenticated Runtime API and the admin API on every interface. Rejected.
- `NetworkMode: host`: Linux only, loses isolation; rejected.
- A wildcard DNS responder handed to containers: needed only for hostname endpoints inside functions; IP literals avoid the need. Deferred to phase 2 behind an ADR.
- Rewriting emulator-origin URLs inside function environment variables: silent mutation of user data that misses payloads and objects; not in phase 1.

## Consequences

- The "bind 127.0.0.1" default holds for the main listener; the extra listener is scoped to the bridge and documented as inside the local Docker trust domain.
- Queue URLs echo the request Host (ADR 0013), so a `GetQueueUrl` made inside a function returns a reachable host; JS SQS clients using a host-minted URL must construct the client with `endpoint` (documented).

## Amendments (review, 2026-10-08)

- `DockerDesktop{HostAddr}` is specified (product M1): no container listener; the main and Runtime API listeners stay on `127.0.0.1` because Desktop forwards `host.docker.internal:<port>` to the host's loopback; the IPv4 literal handed to functions is resolved once per daemon by running the function's own image with `getent ahostsv4 host.docker.internal` (the command as amended in wave 2 below), cached, with `192.168.65.254` as the documented fallback. Whether Docker Desktop becomes a tested topology is open decision 25.1 in the architecture (a human macOS run; agents only have Linux).
- `InContainer{Network, Self}` is CI-tested alongside Linux bridge (product C2): the main listener binds `0.0.0.0` inside the container under the config state `Exposed{ContainerPublished}`, accepted only when this topology is detected, with the published port as the exposure control; function containers and DynamoDB Local attach to understudy's network and are addressed by container IP; the smoke test runs inside the image.
- Windows is supported through WSL2 only (product m9).

## Amendments (implementation, wave 2)

- Rootless Podman is unsupported in phase 1, like rootless Docker: `Addresses` returns an `UnsupportedError` naming the remedy. Its pasta gateway is not a path to the host, and `host.containers.internal` maps to an address that reaches only the host's external interface (`containers.md 8.1` row 7), which understudy does not bind; any address handed to functions would time out.
- `DockerDesktop` has the shape `{Host DesktopHost}`, a sum of `ProbedHost{Addr, Cleanup}` (the address the probe printed; `Cleanup` reports a probe container that could not be removed) and `FallbackHost{Cause}` (the documented `192.168.65.254`, with the reason the probe failed for `doctor`). Only a successful probe is cached per daemon.
- The probe sets the container's Entrypoint to `getent ahostsv4 host.docker.internal` instead of passing `Cmd`: the Lambda base images' entrypoint rejects more than one argument, and glibc's `getent hosts` prints only the IPv6 address of a dual-stack name.
