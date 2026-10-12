// Package lambda implements AWS Lambda: functions, versions and aliases,
// permissions, Invoke, SQS event source mappings and function URLs.
//
// New binds the control plane: functions and their code and
// configuration, versions, aliases, resource policies, reserved
// concurrency, tags, function URL configurations, asynchronous invocation
// configurations and SQS event source mappings, with the validation texts
// of docs/research/phase1/lambda-api.md and lambda-events.md. The data
// plane is an Execution built beside the Service (NewExecution): Invoke
// in its three modes, the asynchronous queue with its retries, function
// logs delivered to CloudWatch Logs, reset draining and hot reload; the
// decisions it takes are listed at the top of invoke.go. Until the server
// builds an Execution, Invoke answers 501 in Lambda's error format. The
// SQS poller and the function URL data plane are not bound yet.
//
// Provisioning states follow the lambda.stateTransitions switch
// (ARCHITECTURE.md 9): with the default, a function is Active and an
// update Successful when the call returns, and a mapping Enabled or
// Disabled; with clock, Pending, InProgress and the mapping transitions
// last 10 ms of emulator time, and calls inside the window get
// ResourceConflictException or ResourceInUseException.
//
// Decisions the architecture leaves open:
//
//   - An image function is Pending until its image is present, in either
//     mode: the pull is tracked work started by a timer due at once, so the
//     create call returns at once and Rearm restarts a pull a snapshot load
//     interrupted. A failed pull is State Failed with ImageAccessDenied for
//     an image that cannot be pulled (InvalidImage otherwise) and a reason
//     naming the image; with no Docker daemon configured every image
//     function fails that way. A failed update keeps the previous image.
//   - A Failed function also reports LastUpdateStatus Failed with the same
//     reason, and refuses updates with "The function is currently in the
//     following state: Failed", the wording recorded for Pending.
//   - Zip packages are checked when they arrive (readable, at most
//     52,428,800 bytes zipped when sent inline and 262,144,000 claimed
//     unzipped) and kept in the blob store, one reference per version that
//     runs them; codestore extracts a package when an environment first
//     runs it. A package named by S3Bucket and S3Key is read with GetObject
//     through the kernel for the caller (Via LambdaCode), and S3's refusal
//     answers as AWS's "Error occurred while GetObject" text. GetFunction's
//     Code.Location, a
//     presigned S3 URL on AWS, is a link under /_understudy/lambda-code/
//     that the raw DownloadFunctionCode serves while the version still
//     runs that package.
//   - The creatable runtimes are the enum recorded in AWS's answer on
//     2026-01-12; the runtimes the model lists before them are deprecated,
//     and anything else is outside the enum. Deprecation does not follow
//     the emulator clock.
//   - Every layer reference is refused: understudy publishes no layers.
//   - RevisionIds and function URL ids are derived from the scope and a
//     per-scope serial in the state, so they are deterministic under a seed
//     without a new identifier stream, and never repeat until a reset.
//   - A function URL is http://<url-id>.lambda-url.<region>.localhost:<port>/
//     on the port the client addressed.
//   - AddPermission stores each statement as the JSON it returned, written
//     by policydoc in Lambda's element order; GetPolicy joins them in
//     insertion order. An account principal becomes its root ARN.
//   - An event source mapping's queue is checked with GetQueueUrl and
//     GetQueueAttributes through the kernel, as the function's execution
//     role, before the mapping is stored; a queue in another region reads as
//     missing. Mappings outlive their function, as AWS's do.
//   - Deleting a mapping answers with State Deleting; with the default
//     transitions the mapping is already gone.
//   - The admin API's Drainer, HotReloader and ImagePuller are the
//     Execution, not the Service, whose Drain has nothing to stop. When
//     the Execution binds a hot-reload overlay, UpdateFunctionCode of a
//     zip function under it records the shadowed-update Advisory.
//   - Publishing while $LATEST waits for an image update publishes that
//     image, the new version waiting for it too; UpdateFunctionCode with
//     Publish therefore always yields the image it names. $LATEST's
//     generation advances again when the image lands, so no environment
//     started during the pull is reused.
//   - Deleting an alias deletes the resource policy, function URL and
//     asynchronous invocation configuration it qualifies. Deleting an alias
//     or a published version that does not exist succeeds, as AWS's does.
package lambda
