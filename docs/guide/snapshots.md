# Snapshots and persistence

understudy's whole state saves to one archive: every resource in every service, account and region, object bodies and Lambda code, the clock, the fault rules, and the positions of the seeded identifier streams. Load it to return to that moment. The archive is deterministic, so the same state written by the same build gives the same bytes: you can commit snapshots as test fixtures and diff them.

## Save, load, inspect, diff

```sh
understudy snapshot save before.tar
aws sqs create-queue --queue-name extra
understudy snapshot save after.tar.gz
understudy snapshot diff before.tar after.tar.gz
understudy snapshot load before.tar
```

A name ending in `.gz` or `.tgz` is gzipped. `--include-journal` adds the [journal](journal.md), which is history and left out by default.

`inspect` prints the manifest without loading anything:

```text
writer        understudy dev
models        api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201
format        archive format version 0
clock         2027-01-01T00:01:35.021498914Z running
seed          none
journal next  52
blobs         embedded
late data     dynamodb
fault rules   saved in the archive
journal       not saved
sections      6
  services/iam/000000000000.json  v1  sha256:808513a96fbf  0 blob(s)
  services/s3/buckets/demo-bucket.json  v1  sha256:0ad7ef8f4211  2 blob(s)
  ...
```

`diff` compares two archives section by section and prints each resource-level change:

```text
+ services/sqs/000000000000/us-east-1.json /data/queues/extra: {"attributes":{...},"name":"extra"}
```

Two saves with calls between them also differ in the manifest's counters, which are not resource state: `manifest.json /journalNext`, the id the journal assigns next, and under `serve --seed` the `manifest.json /positions/<stream>` of each identifier stream the calls drew from. `diff` adds a `note:` line explaining each.

`diff --normalize` masks what differs between two runs of one workload (request ids, receipt handles, wall times, hosts, accounts, event ids, `journalNext`), so two runs of a seeded test should show no differences. It keeps the stream positions, which one seed and one workload reproduce. The wall times include `manifest.json /cutWall`, the wall time a `serve --persist` checkpoint records and `snapshot save` does not, so two checkpoints of the same state compare equal:

```text
$ understudy snapshot diff a.snapshot b.snapshot
~ manifest.json /clock/at: "2026-11-13T20:13:56.916283974Z" -> "2026-11-13T20:14:02.15004406Z"
~ manifest.json /cutWall: "2026-11-13T20:13:56.916360254Z" -> "2026-11-13T20:14:02.15020598Z"
$ understudy snapshot diff --normalize a.snapshot b.snapshot
the archives are identical
```

Loading replaces all state, stops running Lambda environments, and sets the clock to the snapshot's time, even if that is earlier. It leaves the journal as it is: the journal is history, not state (`docs/ARCHITECTURE.md` 10). DynamoDB tables are exported through DynamoDB Local when the snapshot is saved and re-created from the archive when it is loaded.

An archive is capped at 1 GiB, counted decompressed. A save that would pass the cap is refused with nothing saved, and a larger archive is refused on load, at start or under `--persist`. Raise the cap up to 4 GiB with `serve --snapshot-max-bytes` (`UNDERSTUDY_SNAPSHOT_MAX_BYTES`, file key `snapshotMaxBytes`).

## Snapshots as fixtures

Build an expensive state once (a Terraform apply, a seeded dataset), save it, and start every test run from it:

```sh
understudy serve --load-snapshot fixtures/base.tar
```

or load it between tests with `understudy snapshot load`.

The archive format is version 0 until the first release. An archive whose service sections were written by a schema version this understudy does not read is refused with a message naming the versions, never loaded wrongly. DynamoDB and Lambda also read the sections some earlier versions wrote, so their saved state survives an upgrade.

## Persistence across restarts

Persistence is off by default. With `--persist`, understudy keeps its state in a directory and restores it at the next start:

```sh
understudy serve --persist ./.understudy
```

It writes a checkpoint every 30 seconds of wall time while state changes (`--persist-every`) and on a clean shutdown, so a crash loses at most that interval. A change is anything a restart restores: resources, fault rules, the clock's mode or a frozen clock's time, a seeded id draw, or a journaled event, so any API call counts, read-only ones included. Emulator time passing on a running or scaled clock, and an advance of one, are not changes on their own: an idle server writes nothing, and such an advance is saved with the next change or at shutdown. On a restart, a running clock keeps its offset from wall time at the last checkpoint, so an `understudy clock advance` survives however long the server was stopped; a frozen clock holds its time, and a scaled one resumes from its saved time. `snapshot load` keeps the archive's time in every mode. The directory is locked while a server uses it. Under `--persist`, DynamoDB Local keeps its tables in the same directory.

With persistence on, `--on-server` saves to and loads from the server's own `<dir>/snapshots/`, which avoids sending the archive over the connection:

```sh
understudy snapshot save fixtures/a.tar --on-server
understudy snapshot load fixtures/a.tar --on-server
```

`GET /_understudy/v1/snapshots` lists the archives there (`admin.Client.Snapshots` in Go); without `--persist` it answers `503`:

```sh
curl -s http://127.0.0.1:4566/_understudy/v1/snapshots
```

```json
{"snapshots":[{"path":"fixtures/a.tar","bytes":3072,"modified":"2026-10-26T21:33:34.758371515Z"}]}
```

After a crash, `understudy cleanup --persist ./.understudy` removes the containers the crashed server left; it refuses while a server is still running there.

## Resetting

```sh
understudy reset
understudy reset --service sqs --account 111111111111 --region eu-west-1
understudy reset --clock --faults
```

A reset drops state, all of it or for the services, accounts and regions named, and keeps the clock and fault rules unless `--clock` or `--faults` is given. It is faster than a restart: pulled images stay, and DynamoDB Local keeps running with its tables dropped.

## Treat archives as secrets

A snapshot or a `--persist` directory holds everything needed to read every secret and object understudy stores, including KMS key material. Keep them out of public places when they hold real-looking data.
