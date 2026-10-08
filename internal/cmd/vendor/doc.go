// Package vendor is the model vendoring tool run with go run
// ./internal/cmd/vendor. It touches the network only when a human runs it; its
// -verify flag works offline. The package holds only this comment until the
// tool lands; its main file then replaces the package clause with package main.
package vendor
