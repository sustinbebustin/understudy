// Package dynamodb implements the DynamoDB front: a proxy to an
// understudy-managed DynamoDB Local container plus shims for what Local lacks,
// such as TTL expiry on the emulator clock, tags and continuous backups.
package dynamodb
