package awsquery

import "github.com/sustinbebustin/understudy/internal/protocol/smithy"

// elementName is m's Query key and XML element name: its xmlName, else
// fallback (the member name, or member, key, value and entry for the
// collection slots).
func elementName(m *smithy.Member, fallback string) string {
	if m != nil && m.XMLName != "" {
		return m.XMLName
	}
	return fallback
}

// listMember returns the member of list schema s.
func listMember(s *smithy.Schema) *smithy.Member {
	if len(s.Members) == 0 {
		return nil
	}
	return s.Members[0]
}

// mapMembers returns the key and value members of map schema s.
func mapMembers(s *smithy.Schema) (key, value *smithy.Member) {
	for _, m := range s.Members {
		switch m.Name {
		case "key":
			key = m
		case "value":
			value = m
		}
	}
	return key, value
}

// timestampFormat is m's effective timestampFormat, date-time by default,
// the awsQuery default for both requests and responses.
func timestampFormat(m *smithy.Member) smithy.TimestampFormat {
	if m.TimestampFormat != smithy.TimestampProtocolDefault {
		return m.TimestampFormat
	}
	if m.Target != nil && m.Target.TimestampFormat != smithy.TimestampProtocolDefault {
		return m.Target.TimestampFormat
	}
	return smithy.DateTime
}

func formatName(f smithy.TimestampFormat) string {
	switch f {
	case smithy.HTTPDate:
		return "http-date"
	case smithy.EpochSeconds:
		return "epoch-seconds"
	case smithy.DateTime, smithy.TimestampProtocolDefault:
	}
	return "date-time"
}

// policyDocument is IAM's policyDocumentType. IAM sends every member of
// this shape percent-encoded (iam-sts-kms.md 3.3), and botocore decodes
// exactly the members of a shape so named (wire-json-query.md 8.4).
const policyDocument smithy.ShapeID = "com.amazonaws.iam#policyDocumentType"
