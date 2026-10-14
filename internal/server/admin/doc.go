// Package admin serves the admin API under /_understudy/v1/ (ARCHITECTURE.md
// 17, ADR 0017) over the kernel, journal, snapshot, fault, clock and access
// packages. The wire types and the error vocabulary are the public admin
// package's, so the handlers and admin.Client cannot drift.
//
// Decisions the architecture leaves open:
//
//   - Routing reads the escaped path, as the edge does, and matches literal
//     segments exactly; only the "{name}" segments of a route (a fault id, a
//     function name, a subscription ARN) are unescaped. A path that matches a
//     route under another method answers 405 with invalid_request and Allow;
//     one that matches none answers 404 with the listener's routes.
//   - Errors carry three optional members beside code, message and hint:
//     field (the request member at fault), known (the values allowed
//     instead, such as the error shapes an operation really sends, or the
//     members a body may hold when it holds another) and pending (settle's
//     outstanding work by kind). The closed code set adds unauthorized,
//     which the token check needs; admin.ErrorCode holds the one table from
//     a code to its HTTP status and CLI exit code.
//   - Hints name the CLI command first and the HTTP route in parentheses,
//     since the CLI prints them verbatim and other clients need the route.
//   - A configured token guards every endpoint on the main listener,
//     health included; Container serves GET health and GET clock without it,
//     for function code.
//   - The main listener refuses, before the token check, any call that
//     carries Origin or a Sec-Fetch-Site other than "none": headers only
//     browsers send, and that page scripts cannot drop. With no token on a
//     loopback bind, that is what keeps a web page (cross-site, on a
//     hostname rebound to 127.0.0.1, or served by the emulator itself) from
//     resetting state or reading the journal. A request body that is not
//     empty must also be application/json, the one form a page cannot send
//     cross-site without a preflight.
//   - Reset drains through the Drainer only when it selects the drainer's
//     service and resets the cells under the gate's exclusive side. A Late
//     service's data is dropped through LateResetter for the reset's scope;
//     without that hook it is replaced with nothing by a reset that names no
//     account or region, and a reset that does name one and selects the
//     service is refused with unavailable before anything is dropped, since
//     ImportLate replaces a service's late data whole. Under the gate each
//     selected service is exported before and after its reset, and the
//     blob references that disappear (a multiset difference) are released
//     through Options.Blobs once the gate is free, as a snapshot load does
//     for the state it replaces; services therefore do not release blob
//     references in Reset themselves. Every reset then clears the scheduler
//     and rearms every service, so no timer of dropped state survives it.
//     An unfiltered reset also forgets the access record. "clock": true
//     also restores the clock to how serve started it; "faults": true
//     clears the rules.
//   - Reset, snapshot save and snapshot load are serialized by one handler
//     lock, held from the drain or cut through the late steps, so a reset's
//     late reset cannot land between a save's cut and its late exports, or
//     inside a load. The coordinator already serializes save against load;
//     the periodic --persist checkpoint runs outside this handler and
//     writes without late data, so it does not take the lock.
//   - Every route lists the query parameters it reads, and any other is
//     refused with invalid_request before the endpoint runs; a route that
//     reads none, such as POST /reset, refuses every parameter, so a filter
//     misplaced in the query does not widen a destructive call. HEAD is
//     served as GET except on the endpoints that wait on later events
//     (journal/stream, settle), which answer 405.
//   - Clock mode changes made here are journaled as ClockChanged events.
//     Set is AdvanceTo: earlier instants are refused with invalid_request,
//     naming reset and snapshot load as the ways back.
//   - Fault rules are read with fault.Rule's own strict JSON form and shown
//     through admin.FaultRule by reading that JSON back, so the public form
//     mirrors the internal one by construction; admin.FaultRule refuses a
//     member its action's or trigger's kind does not take, as fault.Rule
//     does. The rule id comes from the path; a body naming another id is
//     refused.
//   - POST /snapshots spools the archive to a temporary file before
//     answering, so a failed write is an error response rather than a
//     truncated archive. One PUT runs at a time, since snapshot.Read holds
//     a whole archive in memory; one that arrives meanwhile answers
//     conflict before reading its body. PUT reads and checks the archive
//     before draining and clearing the scheduler; a failed load rearms
//     every service, since timers are a function of the state. A load that
//     replaced the state but could not free the replaced state's blobs
//     succeeded (Load has already rearmed): it answers 200 and logs the
//     leftover blobs. An archive that is damaged or that this build cannot
//     run is conflict, since the file is at fault rather than the request
//     (the CLI exits 1, not with a usage error); one over the size limit is
//     invalid_request; every other failure is internal and says whether the
//     state changed. The path form needs a data directory, takes a local
//     path under it, and writes atomically through a temporary file of its
//     own, so concurrent saves to one path leave a whole archive.
//   - A reset whose clock restore fails still clears the fault rules and
//     the access record it was asked to, which do not depend on the clock,
//     and its error names what was reset and that the clock was not.
//   - GET /resources asks each service for its scopes through Scoper,
//     because kernel.Stateful lists the resources of a scope it is given but
//     names none, then for the resources of each scope the filter covers.
//     Options.Services holds admin.Service values, so every wired service
//     implements Scoper.
//   - Health reports a registered service enabled once it binds an
//     operation and degraded before, since every call to it answers 501.
//     It reports the journal degraded, once, when its file failed or every
//     event id is issued; the kernel logs each refused event.
//   - GET /iam/policy lists every recorded delivery's resource-policy
//     requirement beside the identity policy; they concern the targets, not
//     the principal.
//   - The service hooks (Drainer, HotReloader, ImagePuller, TTLSweeper,
//     SubscriptionTokens) speak *awserr.Error where the failure is the
//     service's (a missing function or subscription) and error where it is
//     infrastructure (Docker, DynamoDB Local); 404 maps to not_found and other
//     4xx to invalid_request. LateResetter is per service, so it is found
//     by asserting on Options.Late rather than wired one by one.
package admin
