// Package smoke holds the in-repo smoke test, built with the smoke tag
// (`just smoke`). It builds the binary, starts `understudy serve` on
// 127.0.0.1:0 with a ready file, a frozen clock, seed 1 and the TLS
// listener, and drives one raw HTTP request per service with net/http:
// SQS over awsJson1_0, SNS publishing to the queue over awsQuery with the
// delivery asserted in the journal, S3 over restXml (a checksum header
// over HTTP, an aws-chunked trailer body over TLS, SSE-KMS), DynamoDB,
// Lambda over restJson1, CloudWatch Logs, Secrets Manager, KMS and
// EventBridge over awsJson1_1, IAM and STS over awsQuery, and CloudWatch
// alarms over awsJson1_0 and rpcv2Cbor. It then asks `understudy iam
// policy` for the smoke principal's policy and reads the coverage report.
//
// With the docker tag as well, the workload invokes its zip function on a
// provided.al2023 runtime, and runs again against understudy inside the
// image built from the repository's Dockerfile, with the Docker socket
// mounted: the InContainer topology.
//
// Choices this package makes:
//
//   - Requests are signed with the access key "test", so every call is the
//     default account's root, the principal whose policy is asked for.
//   - The emulator runs with the test's environment minus UNDERSTUDY_* and
//     AWS_* variables, in an empty working directory, so no configuration
//     of the machine changes what it does. Without the docker tag it runs
//     with --docker disabled, so it starts no container and DynamoDB
//     answers 501 saying it needs Docker; under the docker tag DynamoDB
//     lists tables through DynamoDB Local.
//   - The test trusts the TLS listener's self-signed certificate by
//     reading it from a first handshake, then verifies every request
//     against it.
//   - The in-container run leaves out the TLS step, since the image is
//     started without the TLS listener.
//   - Under the docker tag the function answers with its container's
//     hostname, which Docker sets to the container id. After the graceful
//     stop (SIGINT on the host, SIGTERM from docker stop) no container
//     carrying that container's understudy.dev/instance label may remain,
//     which covers the function environments and DynamoDB Local.
package smoke
