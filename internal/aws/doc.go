// Package aws defines AccountID, Region, Partition, Scope, ARN and RegionSource,
// and the IAM path, user and role name, and STS role session name rules every
// package that holds principals checks against. It imports nothing in the
// repository.
//
// The region table is the aws partition's region names; other partitions'
// regions and local names need regions.extra (Regions.WithExtra). AccountID,
// Region and Partition marshal as text, so persisted state and snapshots keep
// them; a persisted Region is checked for syntax only, because the table that
// admitted it may hold extras. ARN keeps its account verbatim, since
// AWS-managed IAM policies use "aws" there.
package aws
