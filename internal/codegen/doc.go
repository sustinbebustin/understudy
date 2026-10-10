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
//   - codegen/services/<svc>.json names its model's directory in
//     third_party/api-models-aws/UPSTREAM.json ("model"); rawBinding,
//     rawOperations and routeOverrides, and the honored traits listed in
//     notYetEmitted (the overlay's queryKeyAlias and notConfigured among
//     them), fail generation until the all-models generator emits them,
//     rather than being dropped. So does an overlay operation without
//     internalOperation, which no service would reach.
//
// Overlay trait value formats, beyond ARCHITECTURE.md 5.2: evidence is one
// {label, source, validated?} object or a list of them each naming the
// traits it "covers"; validationError is one rule or a list tried in order,
// a rule being {code, style: coral | parameter | missingParameter |
// snsParameter, constraints, reason, enumPolicy} or {enumPolicy} alone;
// queryKeyAlias is {key, decode: receive | ignore}; unknownOperationError is
// {json, query}; patternValidator is {allOf, noneOf} lists of RE2 patterns;
// notConfigured is {error} or {default}.
package codegen
