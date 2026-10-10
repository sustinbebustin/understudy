// Package registry holds the immutable Service, Operation and Route
// descriptors generated from the Smithy models, the BindCoverage sum that bind
// sites state, and the Registry that indexes them.
//
// Descriptors are plain structs so generated code can declare them as
// variables; New validates them at boot, route tables included. Route
// matching follows Smithy specificity routing and extends it for S3: after
// path and query conditions, more required headers win, and a greedy label
// keeps a trailing slash (the key "dir/") that every other pattern ignores.
package registry
