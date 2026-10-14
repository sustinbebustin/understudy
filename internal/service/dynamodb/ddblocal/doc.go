// Package ddblocal manages the DynamoDB Local container (ADR 0014,
// ARCHITECTURE.md 15): the pull of the pinned image by digest with the ECR
// Public mirror as fallback, start with both telemetry opt-outs, the
// readiness probe and removal. The image is pulled at runtime, unmodified,
// and never bundled, mirrored, re-tagged, saved, pushed or patched; it runs
// under Amazon's licence terms, which Notice names when it first starts.
//
// Known deviation, blocked on internal/docker: ADR 0014 publishes the
// container on 127.0.0.1:0 and reads the host port back from the daemon,
// but docker.HostConfig carries no port bindings yet. Until it does, the
// container joins the default bridge and is reached at its bridge address
// (DefaultBridge), which a Linux host routes to and Docker Desktop does
// not; NotReadyError says so. Attached, addressed by container IP, is the
// architecture's own path for the InContainer topology.
//
// The egress test (egress_docker_test.go, -tags docker) runs the pinned
// image with the exact container spec in the network namespace of a
// recording sink on an --internal network. The sink routes every address
// to itself, answers DNS and records every DNS query, TCP SYN and UDP
// datagram the namespace sends, so a connection to any address or port is
// seen; the test checks there is none. A control run without the opt-outs
// shows the sink sees Local's telemetry (dynamodb-local-terms.md 5.3, 7).
package ddblocal
