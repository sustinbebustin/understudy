# testdata

Fixtures shared across packages.

- `parity/<svc>/`: parity recordings of AWS behavior, each labeled with its evidence level.

`pgregory.net/rapid` failure files do not live here. rapid writes a failing case to `testdata/rapid/<TestName>/*.fail` inside the failing test's own package directory; commit it there as a regression seed and rapid replays it automatically.

Tests never call real AWS. Recordings are made by a human with the record mode of the parity harness and are committed with their label.
