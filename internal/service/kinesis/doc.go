// Package kinesis implements Amazon Kinesis Data Streams, slice 1
// (kinesis.md 13.1): streams with even-split shards, the record log on
// internal/shardlog, shard iterators, retention, tags and the calls
// Terraform makes. Resharding, consumers, encryption, resource policies,
// CBOR and HTTP/2 answer 501.
//
// The service is not wired into the server yet: its operations answer 501
// NotImplemented "not implemented yet" until each ticket builds them.
package kinesis
