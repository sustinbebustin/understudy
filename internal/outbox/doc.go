// Package outbox is the delivery engine: a Stateful outbox cell that makes
// delivery attempts on the emulator clock, routes failures to dead-letter
// queues and runs each attempt through kernel.Invoke.
package outbox
