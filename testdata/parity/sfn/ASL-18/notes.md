# ASL-18 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-18
- account ids masked as <acct> below

## Expected vs AWS

All six are definition errors (ValidateStateMachineDefinition `FAIL`), none is left to run time. Four codes are not in the 3.6 list of documented codes: `DUPLICATE_VARIABLE_NAME`, `RESERVED_VARIABLE_NAME`, `INVALID_VARIABLE_NAME`, `UNDEFINED_VARIABLE`.

- 01 inner scope assigns an outer variable: `DUPLICATE_VARIABLE_NAME` `The variable name 'x' was already defined in a parent scope.` at `/States/M/ItemProcessor/States/B/Assign/x`
- 02 ResultPath `$x` (a variable): `SCHEMA_VALIDATION_FAILED` `Value is not a Reference Path: Initial '$' must be followed by '.' or '['` at `/States/A/ResultPath`
- 03 assign `states`: `RESERVED_VARIABLE_NAME` `Cannot assign to reserved variable '$states'.` at `/States/A` (state, not the Assign key)
- 04 assign `x.y`: `INVALID_VARIABLE_NAME` `Invalid variable name 'x.y': the variable name contains invalid characters.` at `/States/A/Assign/x.y`
- 05 undefined `$nope` (definition or run-time error?): definition error -- `UNDEFINED_VARIABLE` `Variable 'nope' referenced in the field 'v.$' is not defined. Use Assign to define the variable.` at `/States/A/Parameters`
- 06 variable from a sibling branch: definition error -- `UNDEFINED_VARIABLE` `Variable 'y' referenced in the field 'v.$' is not defined. Use Assign to define the variable.` at `/States/P/Branches[1]/States/B/Parameters`
