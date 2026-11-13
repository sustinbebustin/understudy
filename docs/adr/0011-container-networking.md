# ADR 0011: Container networking and listener exposure

Status: accepted, 2026-10-08.

## Context

understudy binds to 127.0.0.1 by default, and SDK calls inside functions must route back to it. A listener on 127.0.0.1 is unreachable from a bridge-network container; `host-gateway` resolves to the bridge gateway IP (`containers.md` finding 4). Go v2 and JS v3 build virtual-hosted S3 URLs for any non-IP endpoint, and `*.localhost` resolves to the container's own loopback; only JS v3 routes SQS calls by `QueueUrl`; `--add-host localhost:host-gateway` makes `localhost:<port>` URLs reach the host for Go, boto3, Node and urllib (`lambda-runtime-contract.md 5 to 6`). Docker Desktop, rootless Docker and Podman differ (`containers.md 8.1`).

## Decision

- `internal/topology` detects the environment once at first Lambda use and returns a sealed `Topology` (`LinuxBridge{Gateway}`, `DockerDesktop`, `RootlessDocker`, `Podman{Rootless}`, `InContainer{Network, Self}`; `Remote{Daemon, Self}` since the amendment of 2026-10-14) that yields the bind address for the container listener and the Runtime API listeners, the IPv4-literal `AWS_ENDPOINT_URL` for functions, and the `ExtraHosts` to add.
- A container listener is created on demand on the topology's gateway address at the main port. It serves the AWS API (and only `GET /health` and `GET /clock` from the admin prefix), never `0.0.0.0`, and can be disabled (`lambda.network: disabled`, control plane only). Per-environment Runtime API listeners bind the same address on ephemeral ports.
- Functions get `AWS_ENDPOINT_URL` as an IPv4 literal (path-style in every SDK, no DNS), `AWS_REGION`, minted execution-role credentials and `AWS_EC2_METADATA_DISABLED=true`. On Linux and Podman, `ExtraHosts: ["localhost:host-gateway"]` is added so `localhost:<port>` URLs minted on the host work inside functions; never on Docker Desktop.
- Linux bridge is first-class and CI-tested; Docker Desktop and Podman are best effort with `understudy doctor` checks; rootless Docker is unsupported, with a specific error.
- A missing first `GET /next` within the init limit produces an error naming the address handed to the container, the detected topology and the three likely causes (loopback-only listener, `docker0` firewall, rootless loopback block).

## Alternatives considered

- Bind the main listener on `0.0.0.0`: simplest, exposes an unauthenticated Runtime API and the admin API on every interface. Rejected.
- `NetworkMode: host`: Linux only, loses isolation; rejected.
- A wildcard DNS responder handed to containers: needed only for hostname endpoints inside functions; IP literals avoid the need. Not built; it would need its own ADR.
- Rewriting emulator-origin URLs inside function environment variables: silent mutation of user data that misses payloads and objects. Not built.

## Consequences

- The "bind 127.0.0.1" default holds for the main listener; the extra listener is scoped to the bridge and documented as inside the local Docker trust domain.
- Queue URLs echo the request Host (ADR 0013), so a `GetQueueUrl` made inside a function returns a reachable host; JS SQS clients using a host-minted URL must construct the client with `endpoint` (documented).

## Amendment 2026-10-08: the Docker Desktop and in-container topologies

- `DockerDesktop{HostAddr}` is specified: no container listener; the main and Runtime API listeners stay on `127.0.0.1` because Desktop forwards `host.docker.internal:<port>` to the host's loopback. The IPv4 literal handed to functions is resolved once per daemon by running the function's own image with `getent ahostsv4 host.docker.internal` (the command as the amendment of 2026-10-10 corrects it), cached, with `192.168.65.254` as the documented fallback. (The probe is withdrawn by the second amendment of 2026-11-06: functions get the documented address.) Whether Docker Desktop becomes a tested topology waits on a manual run on macOS before the first release, since CI only has Linux; the first amendment of 2026-11-06 records that run.
- `InContainer{Network, Self}` is CI-tested alongside Linux bridge: the main listener binds `0.0.0.0` inside the container under the config state `Exposed{ContainerPublished}`, accepted only when this topology is detected, with the published port as the exposure control; function containers and DynamoDB Local attach to understudy's network and are addressed by container IP; the smoke test runs inside the image.
- Windows is supported through WSL2 only.

## Amendment 2026-10-10: rootless Podman is unsupported; the Desktop host probe

- Rootless Podman is unsupported, like rootless Docker: `Addresses` returns an `UnsupportedError` naming the remedy. Its pasta gateway is not a path to the host, and `host.containers.internal` maps to an address that reaches only the host's external interface (`containers.md 8.1` row 7), which understudy does not bind; any address handed to functions would time out.
- `DockerDesktop` has the shape `{Host DesktopHost}`, a sum of `ProbedHost{Addr, Cleanup}` (the address the probe printed; `Cleanup` reports a probe container that could not be removed) and `FallbackHost{Cause}` (the documented `192.168.65.254`, with the reason the probe failed for `doctor`). Only a successful probe is cached per daemon.
- The probe sets the container's Entrypoint to `getent ahostsv4 host.docker.internal` instead of passing `Cmd`: the Lambda base images' entrypoint rejects more than one argument, and glibc's `getent hosts` prints only the IPv6 address of a dual-stack name.
- The second amendment of 2026-11-06 withdraws the probe and with it `ProbedHost` and `FallbackHost`.

## Amendment 2026-10-13: the container-facing listeners accept only their subnet's peers

- The container listener and the per-environment Runtime API listeners accept only peers inside the subnet of the host interface that holds the gateway they bind (`topology.ScopePeers`), and close any other connection with a warning naming the peer. Linux accepts packets for a local address on any interface, so without the check a LAN machine routing the bridge subnet through the host would reach the unauthenticated AWS API, or take invocations with `GET /next`. The subnet comes from the host's interfaces, not Docker's IPAM, so it needs no daemon call and also covers a configured `lambda.network` bridge gateway. Containers on other Docker networks that reached the gateway by chance (for example compose apps using `host-gateway`) are refused, which matches this ADR's scope: the listeners serve function containers on the bridge. Scoping each Runtime API listener to its own container's address is not done, because the listener opens before the container exists and is given its address.

## Amendment 2026-10-13: functions reach the admin API where they share the main listener

The first item was widened on 2026-10-14 from the `InContainer` topology to every topology whose container listener address the main listener already holds.

- Wherever functions share the main listener, a function reaches the whole admin API there, not only the health and clock endpoints the container listener serves. That is the `InContainer` topology, where the main listener binds `0.0.0.0` on understudy's own network; Docker Desktop (next item); and any topology whose container listener address the main listener already holds, on every interface (`--listen 0.0.0.0`) or on exactly that address (`--listen 172.17.0.1` on a Linux bridge), where no container listener opens because it would collide with the main port. Beyond loopback on a host `serve` already requires the admin token, so there the token closes the admin API to functions. A separate listener on another port would not close this, since a hostile function can still dial the main port; the control has to be a peer-address check on the main listener that gives a running function container's IP the container listener's treatment (admin limited to `GET /_understudy/v1/health` and `/clock`, `LocalHosts`). The launcher knows each container's IP only after it starts, so the check must record the IP before the function's init code can dial out, or refuse peers on the function network with no known IP. Until that lands, the exposure is accepted as within the local Docker trust domain.
- Docker Desktop has the same exposure: functions reach the loopback main listener through `host.docker.internal` with no token, and the peer cannot be told apart from a host client. This is accepted as the local Docker trust domain; an admin token (`UNDERSTUDY_ADMIN_TOKEN` or `adminToken`) is the control on the admin API for users who run untrusted function code. (Corrected by the first amendment of 2026-10-14: on Desktop's default loopback listener a token is refused, so the exposure stays.)
- Decision: the exposure stays as described for the first release. A peer-address check cannot distinguish a function from a host client on Docker Desktop, and in the `InContainer` topology it would refuse the other containers on understudy's network that the compose setup makes legitimate admin clients, or need static IPAM to close the window between container start and inspect. The control is the admin token: `UNDERSTUDY_ADMIN_TOKEN` (and, because a token moves the listener under the host rules, `--allow-unauthenticated-aws-api`) makes the admin API refuse function code; the AWS API stays open to it by design. A peer check with reserved addresses is not built; it would need its own ADR.

## Amendment 2026-10-14: the admin token is a control only beyond loopback

- The admin token is the control only beyond loopback. `serve` refuses a token on a loopback listener (`internal/config/draft.go`, `resolveExposure`), and beyond loopback it refuses the token without `--allow-unauthenticated-aws-api`. In the `InContainer` topology the main listener is beyond loopback, so the token with that flag closes the admin API to functions, as the 2026-10-13 decision says. On Docker Desktop the default main listener is loopback, so the 2026-10-13 amendment's "an admin token is the control" does not hold there and the exposure stays. A user can listen beyond loopback with the token and the flag; the Runtime API listeners stay on `127.0.0.1` (`DockerDesktop.Addresses`) and functions reach the main listener through `host.docker.internal`, so they should get `401` from the admin API, at the cost of serving the AWS API to every peer on the network. That setup is untested on Docker Desktop. README.md and SECURITY.md say this.

## Amendment 2026-10-14: the `Remote` topology for a daemon on another host

A `tcp://` daemon (plain, or TLS under `DOCKER_TLS_VERIFY`, `containers.md 2.1`) can be on another host, as a docker:dind CI service is. Detection adds a `Remote{Daemon, Self}` topology after the in-container self inspect: a connection to the daemon whose far end is neither loopback nor its own near end shows a daemon on another host, and `Self`, the near end, is where function containers reach understudy, since they leave the daemon's host through its network.

- The container listener and the Runtime API listeners bind `Self`, scoped by `ScopePeers` to the subnet of the interface holding it. The daemon must be inside that subnet, because its containers' connections arrive from its address; a routed daemon is refused at first Lambda use with `RemoteRouteError`, which says to run understudy on the daemon's network or host.
- When the main listener already binds every interface (understudy in a container, the CI job case) or exactly the container listener's address, no container listener opens: it would collide with the main port, and functions call the main listener, with the exposure the second amendment of 2026-10-13 describes. This applies to every topology whose listener address the main listener already covers.
- Function code is copied in (`PUT /containers/{id}/archive`), never bind-mounted, since the daemon cannot see understudy's paths; no `ExtraHosts`, since `host-gateway` names the daemon's host.
- A daemon reached on loopback, such as a docker:dind port published on this host, is taken to be this host's daemon; functions on it would not reach understudy. `understudy doctor` names the detected topology.
- A host on the daemon's network can reach the container listener as a function can: the same trust as the bridge, extended to that network. Plain TCP to the daemon is the network's exposure, not understudy's; every Docker client accepts it.

## Amendment 2026-11-06: the macOS Docker Desktop run

The maintainer ran the pre-release manual run on a Mac, on Docker Desktop 4.98.0 (Engine 29.9.2, macOS 26.7.1, Apple Silicon, Rosetta on).

- Detection holds: `GET /info` reports `OperatingSystem` exactly `Docker Desktop` (`containers.md 2.2`), and `doctor` and `TestDetectOnThisHost` reported `DockerDesktop`. `*.localhost` resolved to loopback.
- Functions ran from the host: the Docker-tagged Lambda and pool packages passed, as did the smoke workload's invoke. They also ran from the image with the socket mounted, given `--group-add 0`, because a container sees the VM's socket as `root:root` 0660 (`testcontainers.md 8.5`, TC-01).
- The host address was always the documented fallback, `192.168.65.254`. The probe this ADR specifies never ran, because no caller sets `topology.Options.ProbeImage`: the server and `doctor` both pass empty options. The fallback worked. Whether to wire the probe or drop it for the documented address was left open; the next amendment drops it.
- The Desktop failures were in tests and modules, not in `serve`: tests that reached DynamoDB Local at a bridge address, a deadline check without room for the VM's clock skew, and the host's socket group added on Desktop. Each was fixed.
- Decision: Docker Desktop stays supported and best effort, not a tested topology. CI cannot run it (GitHub's macOS runners have no Docker), and one manual run is not continuous coverage. The README and the Lambda guide say "supported, not CI-tested" and name this run.

## Amendment 2026-11-06: the Desktop host address is the documented one

- The `host.docker.internal` probe is withdrawn: `DockerDesktop` has no host field, functions are handed Desktop's documented address, `192.168.65.254`, and `doctor` prints `Docker Desktop (host 192.168.65.254)`. The probe never had a caller, so it never ran on any Desktop, and wiring it would have meant passing an image from both the Lambda launcher and the DynamoDB Local manager, since detection happens before whichever container comes first, with no image at all in `doctor`. Code that has never run on a real Desktop would then go live on a topology CI cannot test, to guard against an address Desktop has not moved in years. If Desktop does move it, the fix is a configuration override for the address, not a probe.

## Amendment 2026-11-13: the admin token beyond loopback on Docker Desktop

A manual run settled the setup the first amendment of 2026-10-14 left untested, on Docker Desktop 4.98.0 (Engine 29.9.2, macOS 26.7.1, Apple Silicon), with understudy built from `main` on 2026-11-13. `GET /info` reported `OperatingSystem: Docker Desktop` and `doctor` reported `Docker Desktop (host 192.168.65.254)`. A Python zip function, handed `AWS_ENDPOINT_URL=http://192.168.65.254:4566`, called STS `GetCallerIdentity`, then sent each of the 28 routes of `routes()` in `internal/server/admin/handler.go` with its own method, an empty body and no token, `POST /reset` last.

- `--listen 0.0.0.0:4566` with `UNDERSTUDY_ADMIN_TOKEN` and `--allow-unauthenticated-aws-api`: every admin route answered `401`, health and clock included; STS answered. From the host, `health`, `clock` and `journal/events` answered `401` without the token and `200` with it.
- `--listen` on one LAN address, same token and flag: the function started and ran, so the Runtime API listeners on `127.0.0.1` were reached as before, but STS and all 28 admin routes were refused at connect (`ECONNREFUSED`). Desktop forwards `192.168.65.254` to the host's loopback, where nothing listened. The host created and invoked the function through a TLS listener on `127.0.0.1:4567`, because the macOS application firewall, once its prompt for the unsigned binary was denied, closed even the Mac's own connections to the LAN address with an empty reply.
- Control, the default loopback listener with no token: STS answered and every admin route reached its handler. Health, clock, faults, journal events and stream, resources, coverage, IAM usage, settle, the UI session and SES messages answered `200`; `DELETE /faults`, `204`; `POST /snapshots` saved a snapshot (`200`); the rest refused the empty body or placeholder name (`400`, `404`, `409`, and `503` from `GET /snapshots`, which needs `--persist`). `POST /reset` and `POST /lambda/images/pull` did not answer within the function's 5 second client timeout: the reset waited to drain the function's own running invocation and was cancelled when the client gave up, leaving the function in place.
- Decision: SECURITY.md names `--listen 0.0.0.0` with the token and the flag as the way to close the admin API to functions on Docker Desktop, and says one LAN address leaves functions unable to reach understudy at all. The first amendment of 2026-10-14 says README.md describes the setup; it never did, and SECURITY.md is the only user-facing place that does.
