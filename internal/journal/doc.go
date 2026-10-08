// Package journal records every API call and cross-service delivery as a
// causally linked Event, keeps them in a ring store with byte accounting, and
// serves queries and the resumable NDJSON stream.
package journal
