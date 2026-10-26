// Package authz is the authorization vocabulary services share: how a
// resource's stored policy is read (PolicyProvider), and the one check of a
// request against a resource policy alone (CheckResource), which function
// URLs apply under every iam.enforce mode, as AWS does, and API Gateway
// will (iam-enforcement-surfaces.md 7, 12.2). The multi-policy decision of
// iam-evaluation.md 12.1 belongs here too, beside them.
package authz
