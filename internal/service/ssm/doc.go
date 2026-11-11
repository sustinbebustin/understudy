// Package ssm implements the Parameter Store subset of AWS Systems Manager:
// the thirteen operations of ssm-parameters.md 12.1 over parameters keyed
// by canonical name per account and region (ssm-parameters.md 12.3), with
// versions, labels and tags. Every other SSM operation stays unbound and
// answers 501.
//
// A Standard SecureString value is sealed by KMS Encrypt with the
// encryption context PARAMETER_ARN (ssm-parameters.md 5.2) through
// kernel.Invoke, outside any cell lock: as the caller for a customer
// managed key, as ssm.amazonaws.com for the default key alias/aws/ssm
// (ssm-parameters.md 5.6).
//
// A committed create, overwrite, label or delete puts a Parameter Store
// Change event on the default bus of its scope through EventBridge's
// PutServiceEventInternal, as ssm.amazonaws.com, queued in commit order
// inside the write and sent before the handler answers
// (ssm-parameters.md 9.2; internal/serviceevent). A refused call queues
// nothing.
package ssm
