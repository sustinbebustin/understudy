package checksum

import (
	"hash/crc64"
	"sync"
)

// crc64NVMEPoly is the bit-reflected form of the CRC-64/NVME polynomial
// 0xAD93D23594C93659, the form crc64.MakeTable takes. Go's crc64 already uses
// an all-ones init and final XOR, so the table alone yields CRC-64/NVME
// (go-toolchain.md 7.2; check value 0xAE8B14860A799888).
const crc64NVMEPoly = 0x9A6C9329AC4BC9B5

// crc64NVMETable is built once: crc64.MakeTable allocates a fresh 2 KiB table
// on every call for a polynomial other than ISO or ECMA.
//
//nolint:gochecknoglobals // a lazily computed immutable table, not mutable state
var crc64NVMETable = sync.OnceValue(func() *crc64.Table {
	return crc64.MakeTable(crc64NVMEPoly)
})
