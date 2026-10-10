// Package launcher creates, labels and removes the Docker containers that run
// Lambda functions.
//
// Start builds the container spec of ARCHITECTURE.md 14.3: the function's
// reserved environment plus execution-role credentials, AWS_ENDPOINT_URL as
// an IPv4 literal and AWS_EC2_METADATA_DISABLED; code mounted read-only at
// /var/task through HostConfig.Mounts (never Binds) with a read-only root
// file system; memory equal to memory plus swap, PidsLimit and nofile at
// Lambda's 1024, json-file logs with rotation, every capability dropped,
// no-new-privileges, the ownership labels of containers.md 9.1, the
// topology's extra hosts and network, and the platform of the function's
// architecture. Exits come from one events subscription with per-container
// waits as the fallback; Logs follows output as timestamped lines;
// EnsureImage pulls once per image and records its digest; Sweep removes an
// owner's leftovers after a crash.
//
// Decisions the architecture leaves open:
//
//   - Code delivery follows the topology: tar copy (with a writable root
//     file system) when understudy runs in a container, bind mounts
//     otherwise. Image functions keep a read-only root either way.
//   - The second trigger of ARCHITECTURE.md 14.3, a code path the daemon
//     cannot see, is detected only when the daemon says so: Docker
//     Desktop's "Mounts denied ... is not shared from the host", on the
//     create or the start. Start then retries with tar copy and the
//     launcher keeps copying from then on, with one warning logged.
//     Colima cannot be detected: it mounts a path outside /Users/$USER as
//     an empty directory (containers.md 5.1), so the runtime finds no
//     bootstrap or handler. doctor is where that check belongs.
//   - /tmp is an anonymous volume, the one writable place under a read-only
//     root. It is disk, like Lambda's /tmp: a tmpfs would charge its pages
//     to the memory limit, and with no swap a 128 MB function writing 100 MB
//     there would be OOM-killed where Lambda succeeds. The volume starts as
//     a copy of the image's /tmp (mode 1777 in the base images) and Remove
//     deletes it with the container. Its size is not enforced: the
//     ephemeral storage size is only validated, because StorageOpt limits
//     the writable layer, not a volume, and the local volume driver sizes
//     only tmpfs volumes.
//   - Function variables are applied over understudy's defaults and under
//     the reserved variables: a function may override AWS_ENDPOINT_URL but
//     never AWS_REGION or the credentials. Env is sorted for stable specs.
//   - There is no Stop beside Remove. Function runtimes running as PID 1
//     ignore SIGTERM, so a graceful stop waits out its grace period and
//     ends in SIGKILL anyway (containers.md finding 7, ARCHITECTURE.md 20).
//     Every drain, shutdown included, is Remove's DELETE ?force=1&v=1.
//   - Start takes a Slot besides the Spec: the environment ID, its Runtime
//     API address and log stream differ per environment, while equal Specs
//     share environments.
//   - Exits replay the daemon's buffered events from the epoch, filtered by
//     this instance's label, so an exit that happens before the
//     subscription opens is not lost; a reconnect resumes from the last
//     event's time and drops the replayed duplicates, after a backoff of
//     100 ms doubling to 5 s. When the stream cannot be reopened, or five
//     streams in a row end without an event, every container is waited
//     on instead; one created but not yet started only once it starts,
//     since the daemon reports it as not running.
//   - An OOM kill is the oom event, State.OOMKilled, or exit 137 with no
//     kill event: cgroup v2 hosts often report neither positive signal,
//     and every other SIGKILL of a tracked container goes through the
//     daemon (diagnose has the reasoning). With per-container waits there
//     are no kill events, so an outside docker kill reads as an OOM kill.
//   - An architecture mismatch is "exec format error" in the output, or
//     exit 126 when the declared architecture differs from the daemon's;
//     exit 126 on the host's own architecture stays a plain exit (a
//     non-executable bootstrap also exits 126). A daemon on a machine
//     Lambda does not run (s390x, ppc64le) never yields a mismatch.
//   - EnsureImage never replaces a local image by pulling when the pull
//     could only come from AWS (an ECR reference) or from nowhere (a
//     locally built image with no repository digest): both fail with a
//     typed error naming the architectures instead.
//   - The runtime image table pins the multi-architecture index digest per
//     runtime; images run as repository@digest. provided.al2023 and
//     python3.13 are the digests the research ran against.
//   - Sweep uses the launcher's own ownership: it removes the owner's other
//     instances, never this one's and never another owner's.
package launcher
