// Package topology detects the container networking topology (Linux bridge,
// Docker Desktop, rootless, Podman, running inside a container) and derives
// the addresses containers use to reach the emulator.
//
// Detector.Detect runs at first Lambda use (ADR 0011, containers.md 8).
// Where the architecture leaves a choice open, this package decides:
//
//   - Rootless Podman is unsupported like rootless Docker: its gateway is
//     not a path to the host and host.containers.internal reaches only the
//     host's external address, which understudy does not bind. Addresses
//     returns an *UnsupportedError naming the remedy instead of an address
//     that would time out.
//   - The Docker Desktop probe follows ARCHITECTURE.md 14.5 and ADR 0011: it
//     overrides the image's entrypoint with `getent ahostsv4
//     host.docker.internal`, because the Lambda base images' entrypoint
//     rejects more than one argument and `getent hosts` prints only the
//     IPv6 address of a dual-stack name. A probe that cannot run falls back
//     to 192.168.65.254 and keeps the reason in FallbackHost for doctor.
//     The Detector remembers a successful probe per daemon (Info.ID); a
//     failed one is retried on the next detection. A probe that printed its
//     address but whose container could not be removed still counts as
//     successful; the removal error is kept in ProbedHost.Cleanup for
//     doctor.
//   - Understudy's own container is found by the container ID in
//     /proc/self/mountinfo (the root of the /etc/hostname, /etc/hosts or
//     /etc/resolv.conf mount, under Docker's containers/<id>/ or Podman's
//     overlay-containers/<id>/), then by hostname, each checked against
//     the inspected container's ID or Config.Hostname. A user-defined
//     network is preferred over the default bridge; a container on the host
//     network is detected as the host. Container
//     markers with no matching container on the daemon mean the daemon runs
//     inside understudy's own container (Docker-in-Docker), so detection
//     continues as on a host.
//   - A tcp:// daemon is Remote when a connection to it shows it on another
//     host: its address is neither loopback nor the one the connection
//     leaves from. Function containers then leave the daemon's host through
//     its network, so they reach understudy at that leaving address (Self).
//     A daemon reached on loopback, such as a port published from a
//     docker:dind container on this host, is taken to be this host's.
//     Understudy's own container on the daemon still wins (InContainer).
//   - Addresses carries FunctionNetwork so launchers need not switch on
//     the topology to place function containers.
//   - InContainer opens no container listener, because the main listener
//     binds 0.0.0.0 at the main port and a second listener on Self at that
//     port would collide. Functions there call the main listener, which
//     serves them the whole admin API; a listener on another port would
//     not change that, since functions can still dial Self at the main
//     port, so limiting them is the server's job, by peer address.
package topology
