// Package fault defines fault-injection rules (Rule, Match, Phase, Action,
// Trigger) and evaluates them with a seeded generator against an error
// catalogue.
//
// A Store holds the active rules in the order they were added. Add validates
// a rule against the Catalogue the kernel implements over the registry, so an
// injected error is always one the service really sends; Evaluate tries the
// rules of one Hook in order and returns a Decision. With a seed, each
// Probability rule draws from its own ChaCha8 stream keyed by hash(seed,
// fault-stream kind, rule id): the same rules and call order give the same
// hits, and one rule's draws never shift another's. Export and Import carry
// the rules, hits and stream positions through snapshots.
//
// Decisions the architecture leaves open:
//
//   - The Decision variants that share a name with an Action variant are
//     named apart: a Latency action decides Delay, AfterCommit decides
//     FailAfterCommit and StatusOKError decides FailStatusOK. Every hit carries
//     an Injection{Rule, Hit, Of} for the FaultInjected journal event; Of is
//     the NextN budget, or 0 for a rule without one.
//   - Evaluate takes the Hook calling it, not the Phase: the Request phase has
//     two hooks, BeforeHandler (step 8: Error, Throttle, Latency) and
//     AfterHandler (step 10: AfterCommit, StatusOKError), and the kernel
//     calls AfterHandler only when the handler succeeded. A rule is
//     evaluated, and its budget and stream spent, only at its own hook, so
//     an AfterCommit hit is always one the client sees and an AfterCommit
//     rule never shadows a later Error rule. A Latency rule's Delay carries
//     the decision of the rules after it in Then, so latency composes with
//     an error. Then is decided before Evaluate returns, so its hits are
//     already spent and the caller journals them even when the client
//     leaves during the wait.
//   - In the Delivery phase, MatchInput describes the target of the
//     attempt, so Services, Resource, Accounts and Regions select deliveries
//     by where they land.
//   - The shape-carrying actions (Error, AfterCommit, StatusOKError) need at
//     least one operation, because AWS's error sets are per operation; the
//     Catalogue's Errors must therefore include the generic catalogue of the
//     service's protocol family. A shape may be written without its
//     namespace when the name is unique among the operation's errors; a name
//     that a modeled and a generic shape share must be written in full.
//     Shapes in understudy's own dev.understudy namespace, such as
//     NotImplemented, are left out: AWS never sends them.
//     StatusOKError is limited to S3 CompleteMultipartUpload, CopyObject and
//     UploadPartCopy. Throttle needs a service, listed or implied by an
//     operation, and a zero Identity from Catalogue.Throttle means the
//     service is unknown, which also rejects an unknown service in
//     match.services for any action.
//   - Add rejects a rule that could never match: an operation, or a
//     delivery mechanism's target service, that match.services leaves out,
//     a Delivery-phase service that no mechanism delivers to, and a
//     resource glob whose text before its first * cannot begin "arn:".
//   - RetryableWarning is a method on *Store, not the free function
//     PHASE1-PLAN.md names, because it resolves the rule's error identities
//     through the store's Catalogue, the same way Add does. It names the
//     first retryable identity in a stable order: operations by service and
//     name, then services.
//   - RetryableWarning warns for every StatusOKError rule, whatever its
//     shape, because aws-sdk-go-v2 and botocore read an S3 200 response with
//     an <Error> body as HTTP 500 and retry it (wire-rest.md 3.7). It never
//     warns for a rule with origin internal: only the emulator's own calls
//     see its errors, and their retries follow the delivery engine's
//     policy, not an SDK's.
//   - Error, Throttle, AfterCommit and StatusOKError run in the Request phase,
//     DropDelivery in the Delivery phase, Latency in both. Failing a delivery
//     is a Request-phase rule on the target operation with origin internal,
//     as ARCHITECTURE.md 11 shows for SNS to SQS.
//   - A rule that matches but whose action has no identity for the call (an
//     inconsistent MatchInput) does not fire, and Probability draws happen
//     only for calls a rule otherwise matches.
//   - Rule ids are 1 to 64 characters of letters, digits, '.', '_' and '-',
//     not only dots, because they appear in admin API paths, where net/http
//     cleans "." and ".." segments away; minted ids are "fault-" and 12
//     hex characters from the ids source's fault-stream kind. Add with the id
//     of an active rule replaces it in place and restarts its hits and
//     stream, the semantics of PUT /faults/{id}.
//   - An injected error without a message of its own says it was injected
//     and names the rule, when its shape has a message member. A shape
//     without one carries no message on the wire, so Add rejects an Error
//     action that gives it a message the client would never receive.
//   - Without a seed, streams are keyed from crypto/rand, and Import keeps
//     the recorded positions as counts without replaying.
//   - encoding/json/v2 has no representation for time.Duration, so Latency
//     marshals its fields itself, with Wall as a Go duration string as the
//     config file writes durations.
package fault
