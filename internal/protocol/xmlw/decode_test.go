package xmlw_test

import (
	"encoding/xml"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

const maxBody = 1 << 20

func mustDecode(t *testing.T, doc string) *xmlw.Node {
	t.Helper()
	n, err := xmlw.Decode(strings.NewReader(doc), maxBody)
	if err != nil {
		t.Fatalf("Decode(%q): %v", doc, err)
	}
	return n
}

// S3 request bodies arrive with the S3 namespace from the Go SDK and without
// it in AWS documentation samples; both must decode to the same tree.
func TestDecodeMatchesLocalNamesIgnoringNamespaces(t *testing.T) {
	t.Parallel()
	docs := []string{
		`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`,
		`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`,
		`<s3:Tagging xmlns:s3="urn:any"><s3:TagSet><s3:Tag><s3:Key>k</s3:Key><s3:Value>v</s3:Value></s3:Tag></s3:TagSet></s3:Tagging>`,
		// XML 1.0 (section 4.3.3, Appendix F) allows a byte order mark on a
		// UTF-8 document; editors and PowerShell write one.
		"\xEF\xBB\xBF" + `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`,
		"\xEF\xBB\xBF" + `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`,
	}
	for _, doc := range docs {
		root := mustDecode(t, doc)
		if root.Name != "Tagging" {
			t.Errorf("root name %q, want Tagging", root.Name)
		}
		tagSet, ok := root.Child("TagSet")
		if !ok {
			t.Fatalf("no TagSet in %q", doc)
		}
		tag, ok := tagSet.Child("Tag")
		if !ok {
			t.Fatalf("no Tag in %q", doc)
		}
		key, _ := tag.Child("Key")
		value, _ := tag.Child("Value")
		if key == nil || value == nil || key.Text != "k" || value.Text != "v" {
			t.Errorf("Tag of %q decoded to %+v", doc, tag)
		}
		if len(root.Attrs) != 0 {
			t.Errorf("namespace declarations kept as attributes: %+v", root.Attrs)
		}
	}
}

func TestDecodeGranteeTypeByLocalName(t *testing.T) {
	t.Parallel()
	root := mustDecode(t, `<AccessControlPolicy><AccessControlList><Grant>`+
		`<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee>`+
		`<Permission>READ</Permission></Grant></AccessControlList></AccessControlPolicy>`)
	acl, _ := root.Child("AccessControlList")
	grant, _ := acl.Child("Grant")
	grantee, ok := grant.Child("Grantee")
	if !ok {
		t.Fatal("no Grantee")
	}
	if got, ok := grantee.Attr("type"); !ok || got != "Group" {
		t.Errorf(`Grantee.Attr("type") = %q, %t; want "Group", true`, got, ok)
	}
	if _, ok := grantee.Attr("xsi"); ok {
		t.Error("the xmlns:xsi declaration is reported as an attribute")
	}
	if _, ok := grantee.Attr("missing"); ok {
		t.Error("Attr reports a missing attribute as present")
	}
}

func TestDecodeChildrenInDocumentOrder(t *testing.T) {
	t.Parallel()
	root := mustDecode(t, `<Delete><Object><Key>a</Key></Object><Quiet>true</Quiet><Object><Key>b</Key></Object></Delete>`)
	var keys []string
	for obj := range root.ChildrenNamed("Object") {
		key, _ := obj.Child("Key")
		keys = append(keys, key.Text)
	}
	if !slices.Equal(keys, []string{"a", "b"}) {
		t.Errorf("Object keys %v, want [a b]", keys)
	}
	if len(root.Children) != 3 {
		t.Errorf("%d children, want 3", len(root.Children))
	}
	if _, ok := root.Child("Missing"); ok {
		t.Error("Child reports a missing element as present")
	}
}

func TestDecodeText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, doc, want string
	}{
		{"entities", `<K>a&amp;b&lt;c&gt;&quot;&apos;</K>`, `a&b<c>"'`},
		{"numeric references keep CR", `<K>a&#xD;&#xA;b&#9;</K>`, "a\r\nb\t"},
		{"CDATA", `<K><![CDATA[<raw & text>]]></K>`, "<raw & text>"},
		{"comments are skipped", `<K>a<!-- c -->b</K>`, "ab"},
		{"empty element", `<K/>`, ""},
		{"whitespace kept", "<K> a \n</K>", " a \n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := mustDecode(t, tc.doc).Text; got != tc.want {
				t.Errorf("Text = %q, want %q", got, tc.want)
			}
		})
	}
}

// S3 answers a body that is not well-formed XML with MalformedXML.
func TestDecodeRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, doc string }{
		{"empty body", ""},
		{"whitespace only", "  \n"},
		{"unclosed root", "<A><B></B>"},
		{"mismatched end tag", "<A></B>"},
		{"two roots", "<A/><B/>"},
		{"text after root", "<A/>x"},
		{"text before root", "x<A/>"},
		{"no-break space after the root", "<A/>\U000000A0"},
		{"ideographic space before the root", "\U00003000<A/>"},
		{"line separator after the root", "<A/>\U00002028"},
		{"next line after the root", "<A/>\U00000085"},
		{"DOCTYPE", `<!DOCTYPE A [<!ENTITY e "x">]><A>&e;</A>`},
		{"undefined entity", "<A>&nbsp;</A>"},
		{"unescaped ampersand", "<A>a & b</A>"},
		{"illegal character reference", "<A>&#x1;</A>"},
		{"raw control character", "<A>\x01</A>"},
		{"non-UTF-8 encoding declaration", `<?xml version="1.0" encoding="ISO-8859-1"?><A/>`},
		{"XML 1.1", `<?xml version="1.1"?><A/>`},
		{"duplicate attribute", `<A a="1" a="2"/>`},
		{"XML declaration after the root", `<A/><?xml version="1.0"?>`},
		{"whitespace before the XML declaration", ` <?xml version="1.0"?><A/>`},
		{"XML declaration with an uppercase target", `<?XML version="1.0"?><A/>`},
		{"second byte order mark", "\xEF\xBB\xBF\xEF\xBB\xBF<A/>"},
		{"byte order mark only", "\xEF\xBB\xBF"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, err := xmlw.Decode(strings.NewReader(tc.doc), maxBody)
			var malformed *xmlw.MalformedXMLError
			if !errors.As(err, &malformed) {
				t.Fatalf("Decode(%q) = %+v, %v; want *xmlw.MalformedXMLError", tc.doc, n, err)
			}
			if malformed.Reason == "" {
				t.Error("MalformedXMLError has no reason")
			}
		})
	}
}

// The encoding/xml error behind a MalformedXMLError stays reachable, so logs
// can name the line of the defect.
func TestDecodeMalformedKeepsSyntaxError(t *testing.T) {
	t.Parallel()
	_, err := xmlw.Decode(strings.NewReader("<A>\n<B></C></A>"), maxBody)
	syntax, ok := errors.AsType[*xml.SyntaxError](err)
	if !ok {
		t.Fatalf("Decode returned %v, want it to wrap *xml.SyntaxError", err)
	}
	if syntax.Line != 2 {
		t.Errorf("SyntaxError.Line = %d, want 2", syntax.Line)
	}
	if malformed, ok := errors.AsType[*xmlw.MalformedXMLError](err); !ok || malformed.Err == nil {
		t.Errorf("Decode returned %v, want *xmlw.MalformedXMLError carrying the encoding/xml error", err)
	}
}

func TestDecodeAcceptsCommentsAndInstructionsAroundRoot(t *testing.T) {
	t.Parallel()
	root := mustDecode(t, "<?xml version=\"1.0\"?>\n<?xml-stylesheet href=\"a\"?><!-- c --><A/>\n<!-- c --><?pi data?>\n")
	if root.Name != "A" {
		t.Errorf("root %q, want A", root.Name)
	}
}

func TestDecodeSizeLimit(t *testing.T) {
	t.Parallel()
	doc := "<A>" + strings.Repeat("x", 100) + "</A>"
	limit := int64(len(doc))
	if _, err := xmlw.Decode(strings.NewReader(doc), limit); err != nil {
		t.Errorf("a body of exactly the limit was rejected: %v", err)
	}
	_, err := xmlw.Decode(strings.NewReader(doc), limit-1)
	var tooLarge *xmlw.TooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("a body one byte over the limit returned %v, want *xmlw.TooLargeError", err)
	}
	if tooLarge.Limit != limit-1 {
		t.Errorf("TooLargeError.Limit = %d, want %d", tooLarge.Limit, limit-1)
	}
	// Bytes after a complete root still count: the whole body is the request.
	_, err = xmlw.Decode(strings.NewReader(doc+strings.Repeat(" ", 10)), limit)
	if !errors.As(err, &tooLarge) {
		t.Errorf("trailing bytes past the limit returned %v, want *xmlw.TooLargeError", err)
	}
	// A byte order mark is part of the body.
	if _, err := xmlw.Decode(strings.NewReader("\xEF\xBB\xBF"+doc), limit+3); err != nil {
		t.Errorf("a body with a byte order mark of exactly the limit was rejected: %v", err)
	}
	_, err = xmlw.Decode(strings.NewReader("\xEF\xBB\xBF"+doc), limit+2)
	if !errors.As(err, &tooLarge) {
		t.Errorf("a byte order mark past the limit returned %v, want *xmlw.TooLargeError", err)
	}
	// A negative limit admits nothing and is reported as 0.
	_, err = xmlw.Decode(strings.NewReader("<A/>"), -5)
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 0 {
		t.Errorf("a negative limit returned %v, want *xmlw.TooLargeError with Limit 0", err)
	}
}

func TestDecodeDepthLimit(t *testing.T) {
	t.Parallel()
	nested := func(depth int) string {
		return strings.Repeat("<A>", depth) + strings.Repeat("</A>", depth)
	}
	if _, err := xmlw.Decode(strings.NewReader(nested(xmlw.MaxDepth)), maxBody); err != nil {
		t.Errorf("nesting of MaxDepth was rejected: %v", err)
	}
	_, err := xmlw.Decode(strings.NewReader(nested(xmlw.MaxDepth+1)), maxBody)
	malformed, ok := errors.AsType[*xmlw.MalformedXMLError](err)
	if !ok {
		t.Fatalf("nesting deeper than MaxDepth returned %v, want *xmlw.MalformedXMLError", err)
	}
	if !strings.Contains(malformed.Reason, "deeper") {
		t.Errorf("MalformedXMLError.Reason %q does not name the depth limit", malformed.Reason)
	}
}

// A body that cannot be read (a client that disconnected) is an I/O failure,
// not a malformed document.
func TestDecodePropagatesReadErrors(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset")
	_, err := xmlw.Decode(iotest.TimeoutReader(strings.NewReader("<A>x</A>")), maxBody)
	if !errors.Is(err, iotest.ErrTimeout) {
		t.Errorf("Decode returned %v, want it to wrap %v", err, iotest.ErrTimeout)
	}
	_, err = xmlw.Decode(iotest.ErrReader(broken), maxBody)
	if !errors.Is(err, broken) {
		t.Errorf("Decode returned %v, want it to wrap %v", err, broken)
	}
	if malformed, ok := errors.AsType[*xmlw.MalformedXMLError](err); ok {
		t.Errorf("a read failure was reported as malformed XML: %v", malformed)
	}
}
