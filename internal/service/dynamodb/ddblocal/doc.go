// Package ddblocal manages the DynamoDB Local container (ADR 0014,
// ARCHITECTURE.md 15): the pull of the pinned image by digest with the ECR
// Public mirror as fallback, start with both telemetry opt-outs, the
// readiness probe and removal. The image is pulled at runtime, unmodified,
// and never bundled, mirrored, re-tagged, saved, pushed or patched; it runs
// under Amazon's licence terms, which Notice names when it first starts.
//
// Network says how understudy reaches the container. Published publishes
// its port on 127.0.0.1 with an ephemeral host port read back from the
// daemon, for hosts that cannot route to container addresses (Docker
// Desktop, rootless engines). DefaultBridge reaches it at its default
// bridge address, which a Linux host routes to; it needs no host port.
// Attached, addressed by container IP on understudy's own network, is the
// path for the InContainer topology.
//
// The egress test (egress_docker_test.go, -tags docker) runs the pinned
// image with the exact container spec in the network namespace of a
// recording sink on an --internal network. The sink routes every address
// to itself, answers DNS and records every DNS query, TCP SYN and UDP
// datagram the namespace sends, so a connection to any address or port is
// seen; the test checks there is none. A control run without the opt-outs
// shows the sink sees Local's telemetry (dynamodb-local-terms.md 5.3, 7).
package ddblocal
