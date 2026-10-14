// Package hotreload watches a local directory that a Lambda function's
// code is overlaid from at the execution layer, invisible to the Lambda
// control plane (ARCHITECTURE.md 14.4).
//
// A Watcher polls the tree's file names, sizes, modes and modification
// times every Interval (300 ms by default) and reports a change once the
// tree has stayed the same for Quiet (300 ms by default) after it last
// changed, so a build that writes many files is reported once, after its
// last write. The Lambda service then bumps the overlay's generation and
// the function's next invocation starts cold with the new code.
//
// Decisions the architecture leaves open:
//
//   - Polling with filepath.WalkDir, which does not follow symbolic links,
//     is the whole mechanism: the standard library has no file
//     notification API, and polling also works across the Docker Desktop
//     and Colima file sharing that drops inotify events.
//   - The quiet period is measured in consecutive unchanged polls, so the
//     watcher reads no clock: Quiet must be at least Interval, and is
//     rounded up to whole polls.
//   - Symbolic links are compared as links (their own mode, size and
//     time), never followed, so a link out of the tree cannot make the
//     watcher walk the host's file system.
//   - An entry that cannot be read, or the directory itself going away,
//     is a change like any other; the watcher keeps polling and reports
//     the directory's return the same way.
package hotreload
