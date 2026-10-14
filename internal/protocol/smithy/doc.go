// Package smithy defines the runtime schema types that generated packages
// describe Smithy shapes with, the Serializer and Deserializer interfaces
// the codecs implement, the streaming Body and BodySource, and ShapeID.
//
// The interfaces extend ARCHITECTURE.md 6.1 where a codec cannot work without
// more: WriteStruct names the member that holds a nested structure (nil at
// the top level), unions and map keys have their own calls, every Read call
// positions the codec explicitly, and @streaming blobs pass through ReadBody
// as a Body and WriteBody as a BodySource (ADR 0001). bigDecimal values
// travel as decimal text (BigDecimal) and bigInteger values as *big.Int, so
// neither loses precision. ShapeID is a string type so generated descriptors
// can hold it as a constant.
package smithy
