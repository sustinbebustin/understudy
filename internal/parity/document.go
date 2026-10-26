package parity

import "strings"

// document is a response body parsed by its format (Compare rule 5).
//
//sumtype:decl
type document interface {
	isDocument()
	// source is the text the document was parsed from.
	source() string
}

// jsonDocument is a body that parses as JSON.
type jsonDocument struct {
	root jsonNode
	text string
}

// xmlDocument is a body that parses as one XML element.
type xmlDocument struct {
	root xmlNode
	text string
}

// textDocument is any other body, and a body that does not parse as the
// format its Content-Type or first character names.
type textDocument struct {
	text string
}

func (jsonDocument) isDocument() {}
func (xmlDocument) isDocument()  {}
func (textDocument) isDocument() {}

func (d jsonDocument) source() string { return d.text }
func (d xmlDocument) source() string  { return d.text }
func (d textDocument) source() string { return d.text }

// format is how a body is compared.
type format uint8

const (
	textFormat format = iota + 1
	jsonFormat
	xmlFormat
)

// formatOf is JSON for an application/json or application/x-amz-json-1.0
// or 1.1 Content-Type, XML for text/xml or application/xml; without one of
// those, JSON when the text starts with { or [ after whitespace, XML when it
// starts with <, else text.
func formatOf(contentType, text string) format {
	media, _, _ := strings.Cut(contentType, ";")
	switch strings.ToLower(strings.TrimSpace(media)) {
	case "application/json", "application/x-amz-json-1.0", "application/x-amz-json-1.1":
		return jsonFormat
	case "text/xml", "application/xml":
		return xmlFormat
	}
	t := strings.TrimLeft(text, " \t\r\n")
	switch {
	case strings.HasPrefix(t, "{") || strings.HasPrefix(t, "["):
		return jsonFormat
	case strings.HasPrefix(t, "<"):
		return xmlFormat
	}
	return textFormat
}

// parseDocument parses text by its format; text that does not parse as its
// format is a textDocument.
func parseDocument(contentType, text string) document {
	switch formatOf(contentType, text) {
	case jsonFormat:
		if v, ok := parseJSON(text); ok {
			return jsonDocument{root: v, text: text}
		}
	case xmlFormat:
		if n, ok := parseXML(text); ok {
			return xmlDocument{root: n, text: text}
		}
	case textFormat:
	}
	return textDocument{text: text}
}
