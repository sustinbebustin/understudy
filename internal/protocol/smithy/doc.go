// Package smithy holds the runtime schema descriptors generated from Smithy
// models, including the streaming kind, the Serializer and Deserializer
// interfaces and ShapeID.
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
