// Package authz is the authorization vocabulary services share: how a
// resource's stored policy is read (PolicyProvider), the one check of a
// request against a resource policy alone (CheckResource), which function
// URLs apply under every iam.enforce mode, as AWS does, and API Gateway
// will (iam-enforcement-surfaces.md 7, 12.2), and the multi-policy decision
// of iam-evaluation.md 12.1 (Decide) with the request context it reads
// (BuildContext), the denial messages it maps onto (Denial.Message) and the
// switches that record its unverified choices (Switches).
package authz
