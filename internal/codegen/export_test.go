package codegen

// EmitErrors renders errors_gen.go from an IR built by hand, for the IR
// states the builder never produces.
var EmitErrors = emitErrors

// BuildInputs runs Generate's stages up to the IR, for tests that check
// what the generator would emit across every service.
var BuildInputs = buildInputs
