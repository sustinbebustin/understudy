// Package alarm is CloudWatch metric alarm evaluation and the payloads a
// state change sends, as pure functions: no clock, no store, no kernel.
// The cloudwatch service reads an alarm's bins from its series store,
// calls Evaluate on the emulator clock's evaluation boundaries, and hands
// the builders' output to the outbox and EventBridge
// (cloudwatch-alarms.md 12.3, 12.4).
package alarm
