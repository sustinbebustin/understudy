package sqs

import (
	"crypto/md5" //nolint:gosec // SQS digests are MD5 by protocol (sqs.md 6.3), not for security
	"encoding/binary"
	"encoding/hex"
	"maps"
	"slices"
	"strings"
)

// bodyMD5 is MD5OfMessageBody: the hex MD5 of the UTF-8 body.
func bodyMD5(body string) string {
	sum := md5.Sum([]byte(body)) //nolint:gosec // protocol digest, see the import
	return hex.EncodeToString(sum[:])
}

// attributesMD5 is MD5OfMessageAttributes (and, over system attributes,
// MD5OfMessageSystemAttributes), the algorithm of sqs.md 6.3: attributes
// sorted by name; for each, the length-prefixed name, the length-prefixed
// full data type, one transport byte (1 for String and Number, 2 for
// Binary) and the length-prefixed value; the hex MD5 of the whole. It is
// empty for no attributes, which the responses omit.
func attributesMD5(attrs map[string]Attribute) string {
	if len(attrs) == 0 {
		return ""
	}
	var buf []byte
	field := func(b []byte) {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(b))) //nolint:gosec // attribute sizes are bounded far below 4 GiB
		buf = append(buf, b...)
	}
	for _, name := range slices.Sorted(maps.Keys(attrs)) {
		a := attrs[name]
		field([]byte(name))
		field([]byte(a.DataType))
		if binaryType(a.DataType) {
			buf = append(buf, 2)
			field(a.Binary)
		} else {
			buf = append(buf, 1)
			field([]byte(a.String))
		}
	}
	sum := md5.Sum(buf) //nolint:gosec // protocol digest, see the import
	return hex.EncodeToString(sum[:])
}

// binaryType reports a Binary data type, custom label included
// (Binary.gif).
func binaryType(dataType string) bool {
	base, _, _ := strings.Cut(dataType, ".")
	return base == "Binary"
}
