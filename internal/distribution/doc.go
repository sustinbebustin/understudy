// Package distribution checks the Docker image built from the repository's
// Dockerfile.
//
// The checks are in tests carrying the docker build tag, because they build
// the image and start containers; the package has no runtime code. TestImage
// builds the image once and pins (ARCHITECTURE.md 24):
//
//   - What it is: a static binary on scratch, under 60 MiB unpacked, running
//     as 65532:65532, declaring the entrypoint, command, port, volume,
//     healthcheck and OCI labels the documentation promises, and carrying
//     the CA certificates, /data owned by 65532, a sticky /tmp, and LICENSE
//     and NOTICE under /usr/share/doc/understudy (Apache-2.0 4(a) and 4(d)
//     cover the code generated from AWS's Smithy models).
//   - What it serves: started with only its port published on 127.0.0.1,
//     the image becomes ready on GET /_understudy/v1/health, and that answer,
//     sent without an Authorization header, reports the topology
//     "in-container" and the listener "0.0.0.0:4566". The published port, not
//     a token, is what keeps the admin API local, as
//     Exposed{ContainerPublished} defines. Without the Docker socket, Docker
//     and DynamoDB are disabled with a reason naming "no Docker socket" and
//     Lambda is degraded to its control plane; with it mounted, Docker is
//     enabled.
//
// Where the architecture leaves a choice open, this package decides:
//
//   - A non-root user cannot open a root-owned socket, so the socket run adds
//     the socket's group to the container user (--group-add). The image never
//     runs as root to make the socket readable. The socket run fails, rather
//     than skips, when /var/run/docker.sock is missing, since the docker tag
//     already requires a daemon there.
//   - The image has no shell, so the Dockerfile stages its root filesystem
//     (certificates, /data and /tmp) in a builder stage and copies it as one
//     directory, which keeps each entry's mode and owner.
//   - The Dockerfile's default target cross-compiles on the build platform
//     and copies only the binary into the target-platform image. Its release
//     target copies the binary GoReleaser built instead, so released images
//     ship the checksummed binary.
//   - The image is not built with GoReleaser's dockers and
//     docker_manifests, which GoReleaser v2 deprecates in favor of
//     dockers_v2. That has no skip_push, so .goreleaser.yaml uses one
//     dockers_v2 entry for both platforms and disables it outside snapshot
//     runs. Nothing is pushed either way.
//   - Tests build the image under a unique tag with the label
//     understudy.dev/test=1 and remove containers and the image on cleanup.
package distribution
