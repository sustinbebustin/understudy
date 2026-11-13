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

### Fidelity

**Coverage mark**:
What an operation's binding declares about it: full, stored-only, partial or unbound. The coverage page shows the mark and its note for every operation.
_Avoid_: status, support level

**Stored-only**:
An operation that accepts, stores and returns its input with no runtime effect.
_Avoid_: no-op, stub, mocked

**Unbound**:
An operation with no handler. It answers `501 NotImplemented` in AWS's error format and names itself.
_Avoid_: unsupported, missing, unimplemented

**Evidence label**:
What backs a claim about AWS's behavior, from strongest to weakest: `aws-recorded`, `third-party-recorded`, `aws-doc`, `sdk-source`, `inferred`.
_Avoid_: confidence, source type

**Overlay**:
A constraint AWS enforces that its Smithy model does not declare, written beside the model with an evidence label and applied by the code generator.
_Avoid_: patch, model fix

**Fidelity switch**:
A named behavior choice point, carrying the evidence label of the value understudy implements and the recording that settles it or would. A recording that agrees relabels it aws-recorded and the switch stays; one that disagrees makes it a recorded deviation, labelled by understudy's own evidence. It is deleted only when the choice point leaves the code (ADR 0003, amendment 2026-11-13).
_Avoid_: flag, toggle, option

**Recording**:
A real AWS exchange captured by a maintainer and checked in under `testdata/parity/`.
_Avoid_: capture, snapshot (a snapshot is an archive of emulator state), golden file

**Replay**:
Sending a recording's requests to understudy and comparing its answers with AWS's.
_Avoid_: playback, parity test

**Recorded deviation**:
A behavior where a recording shows AWS doing otherwise and understudy keeps its own, on purpose or for now. It is a fidelity switch, labelled by the evidence for understudy's value and never aws-recorded, whose reason opens "AWS contradicts it:", or a coverage note citing the recording.
_Avoid_: known bug, contradicted switch (contradicted is the coverage page's verdict on a replayed step)

### Causality

**Journal**:
The record of every API call, delivery, invocation, timer and fault, each event carrying the event that caused it.
_Avoid_: log, audit trail, trace (a trace is one tree of the journal)

**Delivery**:
One service handing a payload to another on a producer's behalf: an SNS notification to a queue, an event to a rule's target, a batch to a function.
_Avoid_: dispatch, forward, internal call
