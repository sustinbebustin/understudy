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
// Not implemented yet: the operations are bound and answer NotImplemented
// until their handlers land.
package ssm
