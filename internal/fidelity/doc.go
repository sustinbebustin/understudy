// Package fidelity holds the named behavior switches: the choice points
// where understudy implements one behavior of several possible. Each Switch
// carries an evidence.Label for its value, recorded or not, a provenance
// reference and what settles it, and exposes a type-erased Entry for the
// coverage report. A recorded switch stays, so the parity replay keeps
// checking it; one is deleted only when its choice point leaves the code
// (ADR 0003, amendment 2026-11-13).
package fidelity
