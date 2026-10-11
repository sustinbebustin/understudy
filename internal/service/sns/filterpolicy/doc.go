// Package filterpolicy compiles and evaluates SNS subscription filter
// policies for the MessageAttributes and MessageBody scopes (sns.md 6).
//
// Compile runs the shared Event Ruler core (internal/ruler) with the SNS
// profile: Ruler's grammar and texts, SNS's operator allow-list, and its
// limits of 5 keys, 150 combinations, the wildcard budget and 256 KB. The
// result is an Error whose Reason is the text after "Invalid parameter: "
// on SetSubscriptionAttributes; Subscribe puts "Attributes Reason: "
// before it. Match evaluates a compiled policy against a message's typed
// attributes or its JSON body.
//
// Decisions the architecture and research leave open:
//
//   - Compile returns a typed Error rather than an awserr.Error, because
//     the wire text depends on the call that carries the policy; the sns
//     package renders it as InvalidParameter.
//   - The four conflicts between Ruler and the SNS documentation keep
//     Ruler's behavior (ADR 0015): exists:true accepts null and "", an
//     exists:false rule can match a message with no attributes, a bare
//     number compares numerically, and a malformed "$or" is an error.
//     Switches lists them, with the record-backlog item of each
//     (matching-engines.md 3, 8 and 10), next to the profile's allow-list
//     and limit choices.
//   - A MessageBody-scope message that is not a JSON object is filtered
//     out, not an error (sns.md 6.3). A match the engine gives up on is an
//     error, so the caller neither delivers nor counts it as filtered. SNS's
//     limits leave that reachable: five two-level keys against a 5 KB body
//     that repeats one key over four arrays exhaust the engine's budget.
//   - State keeps a subscription's policy as text, which snapshots carry.
//     A Cache holds the compiled policies by text and scope: the sns
//     package fills it where a policy is set and, after a snapshot load,
//     on the policy's first publish.
//   - Of the filter policy quotas (sns.md 6.5), Subscribe and
//     SetSubscriptionAttributes enforce 200 policies per topic. The 10,000
//     per account is not enforced: it needs a count across every topic of
//     an account, which no single topic write sees.
package filterpolicy
