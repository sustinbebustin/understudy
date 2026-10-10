package xmlw_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

func TestWriterNesting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(w *xmlw.Writer)
		want  string
	}{
		{
			name: "declaration then root",
			write: func(w *xmlw.Writer) {
				w.Declaration()
				w.Start("A")
				w.End()
			},
			want: "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<A></A>",
		},
		{
			name: "start and end with no content keep the end tag",
			write: func(w *xmlw.Writer) {
				w.Start("Prefix")
				w.End()
			},
			want: "<Prefix></Prefix>",
		},
		{
			name: "empty element as root",
			write: func(w *xmlw.Writer) {
				w.Empty("Roles")
			},
			want: "<Roles/>",
		},
		{
			name: "children close the start tag before writing",
			write: func(w *xmlw.Writer) {
				w.StartNS("A", "urn:a")
				w.Attr("id", "1")
				w.Start("B")
				w.Text("x")
				w.End()
				w.Empty("C")
				w.Text("y")
				w.End()
			},
			want: `<A xmlns="urn:a" id="1"><B>x</B><C/>y</A>`,
		},
		{
			name: "prefixed attribute",
			write: func(w *xmlw.Writer) {
				w.Start("A")
				w.AttrNS("xmlns", "p", "urn:p")
				w.AttrNS("p", "k", "v")
				w.End()
			},
			want: `<A xmlns:p="urn:p" p:k="v"></A>`,
		},
		{
			name: "an attribute name may recur on other elements and prefixes",
			write: func(w *xmlw.Writer) {
				w.StartNS("A", "urn:a")
				w.AttrNS("xmlns", "id", "urn:id")
				w.Attr("id", "1")
				w.Start("B")
				w.Attr("id", "2")
				w.End()
				w.End()
			},
			want: `<A xmlns="urn:a" xmlns:id="urn:id" id="1"><B id="2"></B></A>`,
		},
		{
			name: "a prefix declared on an enclosing element and the implicit xml prefix",
			write: func(w *xmlw.Writer) {
				w.Start("A")
				w.AttrNS("xmlns", "p", "urn:p")
				w.Start("B")
				w.AttrNS("p", "k", "v")
				w.AttrNS("xml", "lang", "en")
				w.End()
				w.End()
			},
			want: `<A xmlns:p="urn:p"><B p:k="v" xml:lang="en"></B></A>`,
		},
		{
			name: "one local name in different namespaces",
			write: func(w *xmlw.Writer) {
				w.Start("A")
				w.AttrNS("xmlns", "a", "urn:a")
				w.AttrNS("xmlns", "b", "urn:b")
				w.AttrNS("a", "x", "1")
				w.AttrNS("b", "x", "2")
				w.Attr("x", "3")
				w.End()
			},
			want: `<A xmlns:a="urn:a" xmlns:b="urn:b" a:x="1" b:x="2" x="3"></A>`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := xmlw.New(&buf)
			tc.write(w)
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("wrote %q, want %q", got, tc.want)
			}
		})
	}
}

// Every sequence that cannot produce one well-formed document fails with a
// UsageError naming the call, and nothing reaches the underlying writer, however
// much was written before the failure.
func TestWriterRejectsMalformedSequences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write func(w *xmlw.Writer)
		op    string
	}{
		{"no root element", func(*xmlw.Writer) {}, "Close"},
		{"declaration only", func(w *xmlw.Writer) { w.Declaration() }, "Close"},
		{"unclosed element", func(w *xmlw.Writer) { w.Start("A") }, "Close"},
		{"declaration after start", func(w *xmlw.Writer) { w.Start("A"); w.Declaration() }, "Declaration"},
		{"declaration twice", func(w *xmlw.Writer) { w.Declaration(); w.Declaration() }, "Declaration"},
		{"second root", func(w *xmlw.Writer) { w.Empty("A"); w.Start("B") }, "Start"},
		{"second empty root", func(w *xmlw.Writer) { w.Empty("A"); w.Empty("B") }, "Empty"},
		{"end without start", func(w *xmlw.Writer) { w.End() }, "End"},
		{"end after root closed", func(w *xmlw.Writer) { w.Start("A"); w.End(); w.End() }, "End"},
		{"text before root", func(w *xmlw.Writer) { w.Text("x") }, "Text"},
		{"text after root", func(w *xmlw.Writer) { w.Empty("A"); w.Text("x") }, "Text"},
		{"attribute before root", func(w *xmlw.Writer) { w.Attr("a", "b") }, "Attr"},
		{"attribute after text", func(w *xmlw.Writer) { w.Start("A"); w.Text("x"); w.Attr("a", "b") }, "Attr"},
		{"attribute after child", func(w *xmlw.Writer) { w.Start("A"); w.Empty("B"); w.Attr("a", "b") }, "Attr"},
		{"prefixed attribute after child", func(w *xmlw.Writer) { w.Start("A"); w.Empty("B"); w.AttrNS("p", "a", "b") }, "AttrNS"},
		{"empty element name", func(w *xmlw.Writer) { w.Start("") }, "Start"},
		{"element name with space", func(w *xmlw.Writer) { w.Start("A B") }, "Start"},
		{"element name with markup", func(w *xmlw.Writer) { w.Empty("A/><B") }, "Empty"},
		{"element name with colon", func(w *xmlw.Writer) { w.Start("p:A") }, "Start"},
		{"element name starting with a digit", func(w *xmlw.Writer) { w.Start("1A") }, "Start"},
		{"non-ASCII element name", func(w *xmlw.Writer) { w.Start("Ä") }, "Start"},
		{"attribute name with quote", func(w *xmlw.Writer) { w.Start("A"); w.Attr(`a"`, "b") }, "Attr"},
		{"attribute prefix with colon", func(w *xmlw.Writer) { w.Start("A"); w.AttrNS("p:q", "a", "b") }, "AttrNS"},
		{"namespace element name", func(w *xmlw.Writer) { w.StartNS("A=", "urn:a") }, "StartNS"},
		{"xsi:type element name", func(w *xmlw.Writer) { w.StartXSIType("", "CanonicalUser") }, "StartXSIType"},
		{"repeated attribute", func(w *xmlw.Writer) { w.Start("A"); w.Attr("a", "1"); w.Attr("a", "2") }, "Attr"},
		{"repeated default namespace", func(w *xmlw.Writer) { w.StartNS("A", "urn:a"); w.Attr("xmlns", "urn:b") }, "Attr"},
		{"repeated prefixed attribute", func(w *xmlw.Writer) {
			w.Start("A")
			w.AttrNS("xmlns", "p", "urn:p")
			w.AttrNS("p", "a", "1")
			w.AttrNS("p", "a", "2")
		}, "AttrNS"},
		{"repeated namespace declaration", func(w *xmlw.Writer) {
			w.Start("A")
			w.AttrNS("xmlns", "p", "urn:p")
			w.AttrNS("xmlns", "p", "urn:q")
		}, "AttrNS"},
		{"undeclared prefix", func(w *xmlw.Writer) { w.Start("A"); w.AttrNS("xsi", "type", "x") }, "AttrNS"},
		{"prefix declared on a closed sibling", func(w *xmlw.Writer) {
			w.Start("A")
			w.Start("B")
			w.AttrNS("xmlns", "p", "urn:p")
			w.End()
			w.Start("C")
			w.AttrNS("p", "a", "1")
		}, "AttrNS"},
		{"prefix declared after its use on the same tag", func(w *xmlw.Writer) {
			w.Start("A")
			w.AttrNS("xmlns", "p", "urn:p")
			w.Start("B")
			w.AttrNS("p", "a", "1")
			w.AttrNS("xmlns", "p", "urn:q")
		}, "AttrNS"},
		{"prefix undeclared", func(w *xmlw.Writer) { w.Start("A"); w.AttrNS("xmlns", "p", "") }, "AttrNS"},
		{"prefix xmlns declared", func(w *xmlw.Writer) { w.Start("A"); w.AttrNS("xmlns", "xmlns", "urn:u") }, "AttrNS"},
		{"prefix xml bound elsewhere", func(w *xmlw.Writer) { w.Start("A"); w.AttrNS("xmlns", "xml", "urn:u") }, "AttrNS"},
		{"reserved namespace bound to another prefix", func(w *xmlw.Writer) {
			w.Start("A")
			w.AttrNS("xmlns", "p", "http://www.w3.org/XML/1998/namespace")
		}, "AttrNS"},
		{"reserved namespace as default namespace", func(w *xmlw.Writer) {
			w.StartNS("A", "http://www.w3.org/2000/xmlns/")
		}, "StartNS"},
		{"two prefixes for one namespace on one local name", func(w *xmlw.Writer) {
			w.Start("A")
			w.AttrNS("xmlns", "a", "urn:u")
			w.AttrNS("xmlns", "b", "urn:u")
			w.AttrNS("a", "x", "1")
			w.AttrNS("b", "x", "2")
		}, "AttrNS"},
		{"repeated xsi:type", func(w *xmlw.Writer) { w.StartXSIType("G", "CanonicalUser"); w.AttrNS("xsi", "type", "Group") }, "AttrNS"},
		{"failure after more than a write buffer of output", func(w *xmlw.Writer) {
			w.Start("A")
			w.Text(strings.Repeat("x", 1<<16))
			w.Attr("a", "b")
		}, "Attr"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			w := xmlw.New(&buf)
			tc.write(w)
			if tc.op != "Close" {
				w.Start("After")
				w.Text("after")
				w.End()
			}
			err := w.Close()
			var usage *xmlw.UsageError
			if !errors.As(err, &usage) {
				t.Fatalf("Close returned %v, want *xmlw.UsageError", err)
			}
			if usage.Op != tc.op {
				t.Errorf("UsageError.Op = %q, want %q (%v)", usage.Op, tc.op, err)
			}
			if buf.Len() != 0 {
				t.Errorf("a failed document reached the underlying writer: %q", buf.String())
			}
		})
	}
}

// A document larger than any write buffer reaches the underlying writer whole,
// and a second Close does not write it again.
func TestWriterWritesLargeDocumentOnce(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("x", 1<<16)
	var buf bytes.Buffer
	w := xmlw.New(&buf)
	w.Start("A")
	w.Text(text)
	w.End()
	for range 2 {
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if got, want := buf.String(), "<A>"+text+"</A>"; got != want {
		t.Errorf("wrote %d bytes, want the %d-byte document exactly once", len(got), len(want))
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func TestWriterReportsUnderlyingWriteError(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset")
	w := xmlw.New(failingWriter{err: broken})
	w.Start("A")
	w.Text(string(bytes.Repeat([]byte("x"), 1<<16)))
	w.End()
	if err := w.Close(); !errors.Is(err, broken) {
		t.Fatalf("Close returned %v, want the underlying %v", err, broken)
	}
}
