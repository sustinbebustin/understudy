# codegen

Inputs to the code generator in `internal/codegen`.

- `services/<svc>.json`: per service package name, the model directory in `third_party/api-models-aws/UPSTREAM.json` (`model`), the IAM action prefix (`iamPrefix`), the primary IAM action of operations the rules leave ambiguous (`iamPrimary`, each with a reason), actions the Service Reference omits for an operation (`iamAdditions`, each with a reason and overlay-style evidence), extra protocols, raw bindings, raw operations, route overrides, and routes served beside an operation's modeled one (`additionalRoutes`, each with its query bindings, a reason and overlay-style evidence; ARCHITECTURE.md 5.3). `boundOperations` (optional, sorted) lists the operations a handler binds; other operations' schemas are generated without members to keep the binary small (ADR 0001). When a service lists it, binding an operation means adding its name there and running `just generate`; `internal/server` tests fail until the list matches what the kernel binds. Leave it out of a service that binds every operation.
- `overlays/<svc>.json`: Smithy JSON AST fragments carrying `dev.understudy#` traits, each with evidence.
- `overlays/common.json`: framework errors and the generic vocabulary shared by every service.

Generated output is written to `internal/api` and is never edited by hand. Regenerate with `just generate`.
