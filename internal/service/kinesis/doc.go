// Package kinesis implements Amazon Kinesis Data Streams, slice 1
// (kinesis.md 13.1): streams with even-split shards, the record log on
// internal/shardlog, shard iterators, retention, tags and the calls
// Terraform makes. Resharding, consumers, encryption, resource policies,
// CBOR and HTTP/2 answer 501.
//
// Not built yet, and so unbound (501): ListShards, the data plane
// (PutRecord, PutRecords, GetShardIterator, GetRecords) and enhanced
// monitoring.
package kinesis
