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

`diff --normalize` masks what differs between two runs of one workload (request ids, receipt handles, wall times, hosts, accounts, event ids), so two runs of a seeded test should show no differences.

Loading replaces all state, stops running Lambda environments, and sets the clock to the snapshot's time, even if that is earlier. DynamoDB tables are exported through DynamoDB Local when the snapshot is saved and re-created from the archive when it is loaded.

## Snapshots as fixtures

Build an expensive state once (a Terraform apply, a seeded dataset), save it, and start every test run from it:

```sh
understudy serve --load-snapshot fixtures/base.tar
```

or load it between tests with `understudy snapshot load`. The format is version 0 until the first release: an archive whose service sections were written by a different schema version is refused with a message naming both versions, rather than loaded wrongly.

## Persistence across restarts

Persistence is off by default. With `--persist`, understudy keeps its state in a directory and restores it at the next start:

```sh
understudy serve --persist ./.understudy
```

It writes a checkpoint every 30 seconds of wall time while state changes (`--persist-every`) and on a clean shutdown, so a crash loses at most that interval. The directory is locked while a server uses it. Under `--persist`, DynamoDB Local keeps its tables in the same directory.

With persistence on, `--on-server` saves to and loads from the server's own `<dir>/snapshots/`, which avoids sending the archive over the connection:

```sh
understudy snapshot save fixtures/a.tar --on-server
understudy snapshot load fixtures/a.tar --on-server
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
