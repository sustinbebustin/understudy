// Package codegen is the generator library behind the generated
// internal/api packages (ARCHITECTURE.md 5): model loader, overlay merge,
// shape closure, trait classifier, effective member traits, the ECMA-262 to
// RE2 regex stage, role analysis, the typed IR and the emitters. Generate is
// a pure function of its Inputs; Write and Check put the files on disk or
// report drift. cmd/gen is the command around it.
//
// Choices the architecture leaves open, made here:
//
//   - Serde follows shape roles strictly (ARCHITECTURE.md 5.4): an
//     input-only structure such as SendMessageInput has Deserialize and no
//     Serialize. Tests replay a recorded member sequence into Deserialize
//     (package serdetest).
//   - Identities are generated functions, IdentityX() awserr.Identity, since
//     Go has no struct constants; awserr.Identity literals stay inside
//     internal/api.
//   - Model example inputs become typed Go literals at generation time, so
//     examples_gen_test.go validates them without a wire codec.
//   - dev.understudy#evidence belongs to its overlay entry and is not merged
//     into the shape, so several entries may apply to one shape.
//   - A required member with a default always has a value and gets no
//     presence bit.
//   - \d, \w and \b pass through the regex stage: RE2 gives them the ASCII
//     meaning ECMA-262 gives them without the u flag. \s, \S and . are
//     rewritten to explicit ECMA classes.
//   - The validation policy runtime (render, resolve and their tables) is
//     written once in package validationpolicy, where it is linted and
//     tested, and embedded into every validate_gen.go: generated packages may
//     import only the vocabulary packages, and generated files are not
//     linted.
//   - A descriptor's MaxRequestBytes is the overlay's maxRequestBytes or 0;
//     the protocol default belongs to the edge, which also sees streaming
//     members (ARCHITECTURE.md 4, step 1).
//   - codegen/services/<svc>.json states the IAM action prefix
//     ("iamPrefix"), since the model's arnNamespace is wrong for CloudWatch.
//     It is kept beside the Service Reference table: generation fails when
//     the table has no entry under it, and a cmd/vendor test checks it
//     against the prefixes that tool fetches.
//   - A descriptor lists the primary IAM action first. The Service
//     Reference's lists are alphabetical, so the generator picks it by rule
//     and codegen/services/<svc>.json names it ("iamPrimary") where the rules
//     leave several candidates; generation fails rather than guess. An
//     action of the operation's own name that the service lists under
//     Actions[] but the operation's authorized list leaves out is added as
//     the primary, since the Service Reference's mapping has gaps.
//   - overlays/common.json lists each protocol family's generic vocabulary
//     under its defaults shape's errors, and every real service speaking the
//     family gets GenericErrors(f) in errors_gen.go. awserr.Catalogue stays
//     the runtime copy the edge and the kernel use before a service is
//     known; a test in internal/api checks the two agree for all twelve
//     services. The Query family sets no malformedRequestError: no recording
//     shows what a Query service sends, so the awsquery codec's fallback
//     applies.
//   - codegen/services/<svc>.json names its model's directory in
//     third_party/api-models-aws/UPSTREAM.json ("model"). An overlay
//     operation without internalOperation fails generation, since no
//     service would reach it.
//   - dev.understudy#queryKeyAlias is emitted as smithy.Member.QueryKeyAlias,
//     which the awsQuery decoder applies; an alias naming a key other than
//     the member's own fails generation.
//
// Overlay trait value formats, beyond ARCHITECTURE.md 5.2: evidence is one
// {label, source, validated?} object or a list of them each naming the
// traits it "covers"; validationError is one rule or a list tried in order,
// a rule being {code, style: coral | parameter | missingParameter |
// snsParameter, constraints, reason, enumPolicy} or {enumPolicy} alone, or
// at member level {patternPolicy: handler}, which leaves the member's
// pattern to the handler so its own error text answers;
// queryKeyAlias is {key, decode: receive | ignore}; unknownOperationError is
// {json, query}; patternValidator is {allOf, noneOf} lists of RE2 patterns;
// notConfigured is {error} or {default}.
package codegen
