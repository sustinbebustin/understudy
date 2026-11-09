# understudy

A local emulator of AWS services for development machines and CI. The terms here are the emulator's own; AWS's terms keep AWS's meaning and are not repeated.

## Language

### Time

**Emulator time**:
The clock every AWS-timed behavior reads: visibility, retention, schedules, retries. It is distinct from wall time, which only values a client compares with its own clock use.
_Avoid_: virtual time, fake time, simulated time

**Advance**:
Moving emulator time forward to a target instant, firing every timer due by then in order, each at its own due instant. An advance never waits for the work those timers start.
_Avoid_: tick, jump, fast-forward

**Stepping**:
The part of an advance during which timers fire and emulator time stands at the firing timer's due instant rather than the advance's target.
_Avoid_: mid-advance, in-flight advance

**Tracked work**:
Work the kernel starts on its own goroutines and counts until it finishes: invocations, deliveries, poller steps. Tracked work started while stepping begins once the advance has reached its target.
_Avoid_: background job, task, goroutine

**Settle**:
Waiting until no tracked work is pending and no timer is due. A timer not yet due never counts, so a frozen clock settles.
_Avoid_: drain, flush, quiesce (the test harness's word for a synctest wait followed by a settle)
