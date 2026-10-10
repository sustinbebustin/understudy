// Package awserr defines the AWS-shaped Error every handler returns, its
// Identity, Origin and Violation types, the Coral validation-message renderer
// and the generic error catalogue.
//
// It holds plain values only and never imports a descriptor or the registry.
//
// The generic catalogue gives one Identity per condition and protocol
// family: JSONType carries the com.amazon.coral.service namespace DynamoDB
// sends (clients strip namespaces), QueryCode the Query spelling. A status
// recorded against a real service wins over AWS's CommonErrors pages, which
// disagree with each other; where nothing is recorded the majority of the
// pages wins. A service whose real answer differs states it in its overlay.
// S3 has its own generic vocabulary (InternalError,
// SignatureDoesNotMatch, SlowDown) in its overlay, so the restXml codec
// renders an Internal origin with S3's identity. No AWS recording shows the
// Coral text for invalid UTF-8; the Encoding constraint's wording is
// understudy's own.
package awserr
