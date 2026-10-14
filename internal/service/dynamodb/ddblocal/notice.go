package ddblocal

// LicenceURL is where Amazon publishes the DynamoDB Local License
// Agreement, the terms the pinned image runs under.
const LicenceURL = "https://aws.amazon.com/dynamodb/dynamodblocallicense/"

// Notice is the line logged when DynamoDB Local first starts: understudy
// pulls and runs it unmodified and never bundles it, so its terms are
// Amazon's, not understudy's Apache-2.0 (ADR 0014, 2026-10-14 amendment).
const Notice = "DynamoDB is served by Amazon DynamoDB Local under Amazon's licence terms: " + LicenceURL
