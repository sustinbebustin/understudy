// Package ses implements Amazon SES, API v1 (awsQuery, service ses) and
// API v2 (restJson1, service sesv2), over one store per account and Region
// (ses.md 2.3, switch ses.store.shared-v1-v2).
//
// SES here is journal-only (ses.md 11.2, 11.8): an accepted send is
// validated, checked against the identity and sandbox rules, given a
// MessageId and recorded in the sent-message store that the admin API
// reads. Nothing is ever sent: the package imports neither internal/egress
// nor net/smtp, produces no event or notification, and never completes a
// verification on its own.
//
// Service is the v1 binding and the full kernel.Stateful of the shared
// store; V2 is the v2 binding, a view that exports and lists nothing and
// whose Reset resets the same store.
//
// Every operation is unbound and answers 501 until its stage binds it.
package ses
