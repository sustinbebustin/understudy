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

## Amendments (hardening round 2)

- The container listener and the per-environment Runtime API listeners accept only peers inside the subnet of the host interface that holds the gateway they bind (`topology.ScopePeers`), and close any other connection with a warning naming the peer. Linux accepts packets for a local address on any interface, so without the check a LAN machine routing the bridge subnet through the host would reach the unauthenticated AWS API, or take invocations with `GET /next`. The subnet comes from the host's interfaces, not Docker's IPAM, so it needs no daemon call and also covers a configured `lambda.network` bridge gateway. Containers on other Docker networks that reached the gateway by chance (for example compose apps using `host-gateway`) are refused, which matches this ADR's scope: the listeners serve function containers on the bridge. Scoping each Runtime API listener to its own container's address is not done, because the listener opens before the container exists and is given its address.

## Amendments (hardening round 3)

- Wherever functions share the main listener, a function reaches the whole admin API there, not only the health and clock endpoints the container listener serves. That is the `InContainer` topology, where the main listener binds `0.0.0.0` on understudy's own network; Docker Desktop (next item); and any topology whose container listener address the main listener already holds, on every interface (`--listen 0.0.0.0`) or on exactly that address (`--listen 172.17.0.1` on a Linux bridge), where no container listener opens because it would collide with the main port. Beyond loopback on a host `serve` already requires the admin token, so there the token closes the admin API to functions. A separate listener on another port would not close this, since a hostile function can still dial the main port; the control has to be a peer-address check on the main listener that gives a running function container's IP the container listener's treatment (admin limited to `GET /_understudy/v1/health` and `/clock`, `LocalHosts`). The launcher knows each container's IP only after it starts, so the check must record the IP before the function's init code can dial out, or refuse peers on the function network with no known IP. Until that lands (`docs/plan/DEFERRED.md`, hardening round 3), the exposure is accepted as within the local Docker trust domain.
- Docker Desktop has the same exposure: functions reach the loopback main listener through `host.docker.internal` with no token, and the peer cannot be told apart from a host client. This is accepted as the local Docker trust domain; an admin token (`UNDERSTUDY_ADMIN_TOKEN` or `adminToken`) is the control on the admin API for users who run untrusted function code.
- Decision (pre-tag review, 2026-10-13): the exposure stays as described for the first tag. A peer-address check cannot distinguish a function from a host client on Docker Desktop, and in the `InContainer` topology it would refuse the other containers on understudy's network that the compose setup makes legitimate admin clients, or need static IPAM to close the window between container start and inspect. The control is the admin token: `UNDERSTUDY_ADMIN_TOKEN` (and, because a token moves the listener under the host rules, `--allow-unauthenticated-aws-api`) makes the admin API refuse function code; the AWS API stays open to it by design. A peer check with reserved addresses is a phase 2 option behind an ADR.

## Amendments (pre-tag documentation fixes, 2026-10-14)

- The admin token is the control only beyond loopback. `serve` refuses a token on a loopback listener (`internal/config/draft.go`, `resolveExposure`), and beyond loopback it refuses the token without `--allow-unauthenticated-aws-api`. In the `InContainer` topology the main listener is beyond loopback, so the token with that flag closes the admin API to functions, as the pre-tag review decision says. On Docker Desktop the default main listener is loopback, so the hardening round 3 amendment's "an admin token is the control" does not hold there and the exposure stays. A user can listen beyond loopback with the token and the flag; the Runtime API listeners stay on `127.0.0.1` (`DockerDesktop.Addresses`) and functions reach the main listener through `host.docker.internal`, so they should get `401` from the admin API, at the cost of serving the AWS API to every peer on the network. That setup is untested on Docker Desktop. README.md and SECURITY.md say this.

## Amendment (dependency policy, 2026-10-14)

A `tcp://` daemon (plain, or TLS under `DOCKER_TLS_VERIFY`, `containers.md 2.1`) can be on another host, as a docker:dind CI service is. Detection adds a `Remote{Daemon, Self}` topology after the in-container self inspect: a connection to the daemon whose far end is neither loopback nor its own near end shows a daemon on another host, and `Self`, the near end, is where function containers reach understudy, since they leave the daemon's host through its network.

- The container listener and the Runtime API listeners bind `Self`, scoped by `ScopePeers` to the subnet of the interface holding it. The daemon must be inside that subnet, because its containers' connections arrive from its address; a routed daemon is refused at first Lambda use with `RemoteRouteError`, which says to run understudy on the daemon's network or host.
- When the main listener already binds every interface (understudy in a container, the CI job case) or exactly the container listener's address, no container listener opens: it would collide with the main port, and functions call the main listener, with the exposure of the hardening round 3 amendment. This applies to every topology whose listener address the main listener already covers.
- Function code is copied in (`PUT /containers/{id}/archive`), never bind-mounted, since the daemon cannot see understudy's paths; no `ExtraHosts`, since `host-gateway` names the daemon's host.
- A daemon reached on loopback, such as a docker:dind port published on this host, is taken to be this host's daemon; functions on it would not reach understudy. `understudy doctor` names the detected topology.
- A host on the daemon's network can reach the container listener as a function can: the same trust as the bridge, extended to that network. Plain TCP to the daemon is the network's exposure, not understudy's; every Docker client accepts it.
