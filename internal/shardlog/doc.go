// Package shardlog is the stream log DynamoDB Streams and Kinesis share: an
// append-only shard of records with sequence numbers, positions to read
// from, trimming on retention, and the signed shard iterators that carry a
// reader's position between calls (dynamodb-streams.md 11.1; kinesis.md
// 13.5). It is pure: every instant is a parameter, so the emulator clock
// stays with the services that own the logs.
package shardlog
