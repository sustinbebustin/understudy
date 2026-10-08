# codegen

Inputs to the code generator in `internal/codegen`.

- `services/<svc>.json`: per service package name, extra protocols, raw bindings, raw operations and route overrides.
- `overlays/<svc>.json`: Smithy JSON AST fragments carrying `dev.understudy#` traits, each with evidence.
- `overlays/common.json`: framework errors and the generic vocabulary shared by every service.

Generated output is written to `internal/api` and is never edited by hand. Regenerate with `just generate`.
