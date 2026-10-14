// Package docker is a client for the Docker Engine API over its unix socket
// or TCP, built on net/http. It provides typed errors, stdcopy
// demultiplexing, pull progress and the events stream. Podman's compatible
// socket works too.
//
// The client knows no Lambda concepts; launching, pooling and logs live in
// the Lambda runtime packages. The behavior follows containers.md 2, 9 and
// 10:
//
//   - Transport: an Endpoint is a Unix socket, plain TCP, or TLS, which
//     verifies the daemon against ca.pem and presents cert.pem and key.pem
//     from one directory, the files the Docker CLI reads from
//     DOCKER_CERT_PATH (containers.md 2.1). The endpoint dials, TLS
//     handshake included, so every call, streams too, runs over HTTP/1.1
//     on whatever connection it opens. The certificates are read at each
//     new connection, not by New.
//   - Version: every call is prefixed with the negotiated version, the lower
//     of MaxAPIVersion (1.44) and the daemon's Api-Version from /_ping, and a
//     daemon below Options.APIVersionMin (default 1.41) is refused with a
//     *VersionError. Podman accepts any prefix, so the clamp never relies on
//     a 400 from the daemon. Responses are decoded leniently.
//   - Errors: a status of 400 or more is an *Error carrying the daemon's
//     message; 304 from start and stop is success; a failed connection is
//     an *UnavailableError whose reason (missing socket, refused,
//     permission, unknown host, unreadable certificates, a failed
//     handshake, or TLS on one side only) selects the remediation; a
//     failure inside a started stream is a *StreamError. A plain request
//     to a TLS daemon is answered 400 by Go's HTTP server, which dockerd
//     runs, so that answer is an *UnavailableError too.
//   - Streams: logs, events and waits hold a connection. ContainerWait
//     returns once the daemon has registered the waiter, so a WaitNextExit
//     taken before ContainerStart cannot miss the exit; logs and events are
//     read with Next until io.EOF and closed by the caller.
//   - Cleanup: every object understudy creates carries Labels; Sweep removes
//     an owner's earlier instances and never another owner's objects. An
//     Ownership without an instance or owner is refused (*OwnershipError).
//   - Podman: detected by the Libpod-Api-Version header (Ping.Engine);
//     a create with endpoint settings for more than one network is refused
//     up front with an *EngineLimitError because Podman accepts only one.
//     Its logs carry no Content-Type, so ContainerLogs reads the container's
//     Tty setting to tell framed output from raw, as below API 1.42.
//
// Registry authentication is not sent: phase 1 pulls public images
// anonymously.
//
// The client stays in-repo rather than on the Moby client (ADR 0016,
// dependency policy amendment), so keeping it current is this package's job:
//
//   - Raising MaxAPIVersion: read the Engine API changelog for every version
//     between the current maximum and the new one, and record in
//     containers.md 2.2 each change to an endpoint, field or status this
//     package sends or reads. The negotiated version can still be anything
//     from Options.APIVersionMin up, so a field whose meaning changes at the
//     new version (1.45 changes inspect's Aliases) is read by the negotiated
//     version, as ContainerLogs reads its Content-Type, or the raise waits.
//     Podman accepts any prefix and answers with the semantics of its own
//     compatibility version, so check that too. Then change MaxAPIVersion
//     and the version named above, run the package tests against dockertest
//     at both ends of the window, and run `just test-docker` against a
//     current Docker Engine and Podman.
//   - Adding a field: add only what a caller sets or reads, spelled as the
//     Engine API spells it, with omitempty or omitzero so an unset field is
//     not sent. A request field newer than Options.APIVersionMin is refused
//     or left out when the negotiated version is older, with the version
//     that introduced it noted beside it. Responses decode leniently, so an
//     unread response field needs nothing. Teach dockertest the field, with
//     the status and message the daemon gives for a bad value, and test
//     through it.
//   - Changing how an Endpoint connects: dockertest serves every transport
//     (Options.Transport), so run the change against each, and follow the
//     Docker CLI's reading of DOCKER_HOST, DOCKER_TLS_VERIFY and
//     DOCKER_CERT_PATH as containers.md 2.1 records it, noting a deviation
//     there.
package docker
