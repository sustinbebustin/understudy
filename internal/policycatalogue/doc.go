// Package policycatalogue is the catalogue of AWS managed policies
// understudy ships (ADR 0016, amendment "managed policies";
// iam-evaluation.md 10): each policy's name, ARN, path, type, description,
// default version, dates and document as the AWS Managed Policy Reference
// Guide publishes them, validated, indexed by ARN and in listing order.
//
// The data is third_party/aws-managed-policies/policies.json, which
// internal/cmd/managedpolicies writes and checks; Encode is its canonical
// form and Parse its strict reader. Embedded loads the copy compiled into
// the binary on first use, so starting the emulator does not pay for it.
// The policydoc rules are the generator's -verify check, not this
// package's, so a policydoc gap never stops the emulator from starting.
package policycatalogue
