// Package snapshot reads and writes the versioned, deterministic snapshot
// archive (ARCHITECTURE.md 13, ADR 0009): the manifest, the per-service
// sections, the blobs they pin, the normalizers and the diff, and the
// Coordinator that runs the two-phase save and the mirrored load against
// the store.Gate.
//
// Write produces an uncompressed POSIX tar, entries sorted by name, mtime
// 0, uid and gid 0, mode 0644, optionally gzipped with an empty name and a
// zero mtime, so the same Archive always gives the same bytes. Read parses
// one to the end of its stream, refusing a newer Format before anything
// else, non-local entry names, sections of another schema version, a damaged
// stream and anything the manifest does not account for.
//
// Diff compares two archives entry by entry. With normalization it first
// applies the closed normalizer set ARCHITECTURE.md 12 and ADR 0018 name,
// shared with the parity replayer; nothing else is masked:
//
//   - request ids: the value of a member whose name ends in RequestId (any
//     case, '_' and '-' ignored), and the id in Lambda START, END and
//     REPORT log lines;
//   - receipt handles: any string of AQEB and at least 32 base64
//     characters;
//   - wall-domain values: the values of members named Expiration or Expiry
//     (STS credential expiry; identity's export writes Expiry),
//     CreationDateTime, LastUpdateToPayPerRequestDateTime,
//     ApproximateCreationDateTime, LatestStreamLabel and StreamLabel
//     (DynamoDB Local), the durations of a Lambda REPORT line, and a Running
//     or Scaled clock's time in the manifest;
//   - hosts: the host and port of every http and https URL;
//   - account ids: the account field of every ARN, an account that starts a
//     URL path (SQS queue URLs), the account of a whole store.ScopeKeys
//     value ("<account>/<region>", as the S3 bucket index holds), a 12-digit
//     value of a member whose name ends in Account or AccountId or of an SQS
//     message's senderId (a root sender's account), the segment after
//     services/<svc>/ of an entry name, and the whole value of a member
//     whose name ends in AccessKeyId when it is an AKIA or ASIA key that
//     encodes an account (ids.DecodeAccount), since a minted key differs
//     between runs only by that account;
//   - event ids: the values of members named JournalNext or ending in
//     EventId, and of Parent and Trace inside a Cause object.
//
// The member-name rules skip the members of an object named Tags,
// Variables or Metadata (any case) whose values are all strings: those are
// user-keyed maps (SQS queue tags, Lambda environment variables, S3 user
// metadata), so a tag named Expiry or LastRequestId is user data and still
// compares. Their values' text is normalized like any other string. An
// object with any other value under one of those names is a record keyed by
// a user-chosen name, such as a queue called "tags", and every rule applies
// to it. One case remains: a record whose members are all strings and whose
// user-chosen name is Tags, Variables or Metadata is not masked by name.
// Services that persist another user-keyed map of strings name it with one
// of those three, or the scope rule in normalize.go is extended.
//
// The member-name rules are a naming convention for state (ARCHITECTURE.md
// 13): a persisted ref.EventID member's JSON name ends in EventId, and a
// member whose value is an account's ends in Account or AccountId. A member
// that breaks it shows up in every normalized diff between runs, so it is
// either renamed or listed above by name, as SQS's senderId is: it holds an
// account only for root senders.
//
// Normalizing masks values in place and never reorders an array, because
// array order is meaningful in state such as SQS message order. State that
// sorts an array by a masked value sorts by what the mask keeps, or it
// lists its elements in another order under another account: identity's
// export sorts keys with ids.CompareAccessKeys, which ignores the account a
// key encodes.
//
// Object members and entries that share a normalized name, and any whose
// name normalizing changed, are numbered name#1, name#2, ... in the order of
// their normalized values, so maps keyed by ids line up by content and a
// lone masked key lines up with the first of several. The member-name rules
// read a name without its trailing #n suffixes, so normalizing a normalized
// tree changes nothing.
//
// Decisions the architecture leaves open:
//
//   - Manifest.Clock records the mode's kind and a Scaled factor only. A
//     mode's offset and anchors depend on the saving process's wall clock,
//     so they would make every save differ, and clock.Clock.Restore ignores
//     them.
//   - SectionMeta also carries the section's blob references (store.Section
//     needs them back on load) and a Late flag; Archive keeps late sections
//     apart in Archive.Late. DynamoDB is both Stateful (shim cells) and Late
//     (items), so the archive must say which side loads each section. A
//     late section pins no blobs: no cut pins them and no load retains
//     them, so Write, Read and WriteAfterCut refuse one that does.
//   - Manifest.LateServices lists the Late services a save captured, since a
//     service with no tables writes no section. WriteAfterCut takes
//     WithLateData (`snapshot save`) or WithoutLateData (the --persist
//     checkpoint, where DynamoDB Local owns the items), and Load replaces
//     the late data of the listed services only.
//   - Manifest.Blobs records the blob placement WriteOptions.Blobs chose:
//     "embedded" writes every pinned blob under blobs/sha256/ (`snapshot
//     save`, fixtures); "external" writes none and leaves them in the blob
//     store, as the --persist checkpoint needs with <dir>/blobs (ADR 0009).
//     Read refuses a blob entry in an external archive and returns no blob
//     source; Load then checks, pins and retains the blobs in the
//     Coordinator's BlobStore without importing them, so the --persist
//     startup is NewDisk, Load (which prunes) and nothing else.
//   - Load learns the blob references of the state it replaces by exporting
//     every Stateful service under the gate before ResetAll, and releases
//     them after the retain pass; it pins the archive's blobs until then.
//   - Archive.Journal is bytes, not an io.Reader: a tar header needs the
//     size before the body, and an Archive must write the same bytes twice.
//   - ReadOptions.Schemas checks section versions in Read when set; nil
//     reads any version, for inspect and diff. Load checks them again
//     against the Coordinator's services before changing anything.
//   - The load side of a service is Stateful (ResetAll, Import, Rearm) and
//     Late (ImportLate): the package cannot import kernel, so the server
//     adapts kernel.Stateful's Reset(scope) to ResetAll. Runtime reads and
//     restores the clock, id streams, journal counter and fault rules.
//     RestoreClock runs outside the gate because the clock's step lock comes
//     before the gate in the lock order; RestoreUnderGate restores the rest
//     under the gate with the cells, so no call against the old cells draws
//     an id or a fault decision from the loaded head.
//   - A Cut holds the Coordinator until WriteAfterCut or Release, so a load
//     never lands between a cut and its late exports; a Cut or Load waiting
//     behind it gives up when its context ends. WriteAfterCut refuses
//     a cut that is released or belongs to another Coordinator. Only Head,
//     the Exports and the Pin run under the gate; the duplicate-path check
//     follows the release and hashing waits for Write.
//   - A committed write hands its deliveries to the outbox after releasing
//     the gate, so Cut first waits, gate released, up to EffectsWait of wall
//     time for Effects to report none pending (ADR 0009's settle wait). A
//     cut taken at the timeout carries the count in Cut.PendingEffects for
//     the save to report.
//   - Read holds the whole archive in memory and requires a positive
//     MaxBytes, which caps the decompressed stream and every entry's
//     contents; an archive over the cap is a *SizeError even when its format
//     is newer, because manifest.json sorts after the blobs. Sparse entries
//     are refused as not regular, unread, since archive/tar expands their
//     holes without reading them from the stream. A blob present in the
//     archive that no section pins is refused rather than ignored.
//   - Diff wraps each section as {"version", "blobs", "data"}, parses
//     .ndjson sections as an array of their lines, compares bytes by digest
//     when they are not JSON, and does not compare journals, which compare
//     as canonical trace trees. It returns an error for a manifest Write
//     would refuse instead of comparing a placeholder.
package snapshot
