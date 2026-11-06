// Package enforce is the kernel.Authorizer of iam.enforce advise and enforce
// (iam-enforcement-surfaces.md 12.1). For each (action, resource) pair a call
// needs, it gathers the principal's identity-based policies and the
// resource's stored policy, builds the request context, asks authz.Decide,
// journals what AWS would not tell the caller (unsupplied condition keys,
// denials), and answers a denial as the call's service does: under advise it
// journals the denial and lets the call go on, under enforce it ends the
// call. Evaluate answers the admin API's POST /iam/evaluate under every mode.
package enforce
