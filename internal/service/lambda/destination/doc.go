// Package destination builds what Lambda sends when an asynchronous
// invocation ends: the invocation record an on-success or on-failure
// destination receives, wrapped as the outbox delivery to that destination,
// and the dead letter the function's dead-letter queue or topic receives
// (lambda-destinations-layers.md 3, 4, 5, 11.3, 11.4).
//
// Everything here is a pure function of its inputs: the caller supplies the
// instant, the request id and the object key's UUID, so golden fixtures pin
// the bytes. The Lambda service decides the condition, which configuration
// applies and when a record goes out.
package destination
