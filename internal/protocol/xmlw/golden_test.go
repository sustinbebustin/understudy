package xmlw_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
)

// write runs build against a fresh Writer and returns the document.
func write(t *testing.T, build func(w *xmlw.Writer)) string {
	t.Helper()
	var buf bytes.Buffer
	w := xmlw.New(&buf)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.String()
}

func element(w *xmlw.Writer, name, text string) {
	w.Start(name)
	w.Text(text)
	w.End()
}

func assertGolden(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("document differs from golden\n got: %s\nwant: %s", got, want)
	}
}

// ListObjectsV2: the S3 namespace on the success root, flattened Contents,
// an empty Prefix kept as an element, and a quoted ETag.
func TestGoldenS3ListBucketResult(t *testing.T) {
	t.Parallel()
	got := write(t, func(w *xmlw.Writer) {
		w.Declaration()
		w.StartNS("ListBucketResult", "http://s3.amazonaws.com/doc/2006-03-01/")
		element(w, "Name", "amzn-s3-demo-bucket")
		element(w, "Prefix", "")
		element(w, "KeyCount", "2")
		element(w, "MaxKeys", "1000")
		element(w, "IsTruncated", "false")
		for _, key := range []string{"photos/a&b.jpg", "notes/<draft>.txt"} {
			w.Start("Contents")
			element(w, "Key", key)
			element(w, "LastModified", "2009-10-12T17:50:30.000Z")
			element(w, "ETag", `"fba9dede5f27731c9771645a39863328"`)
			element(w, "Size", "434234")
			element(w, "StorageClass", "STANDARD")
			w.End()
		}
		w.End()
	})
	contents := func(key string) string {
		return `<Contents><Key>` + key + `</Key>` +
			`<LastModified>2009-10-12T17:50:30.000Z</LastModified>` +
			`<ETag>&#34;fba9dede5f27731c9771645a39863328&#34;</ETag>` +
			`<Size>434234</Size><StorageClass>STANDARD</StorageClass></Contents>`
	}
	assertGolden(t, got, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
		`<Name>amzn-s3-demo-bucket</Name><Prefix></Prefix><KeyCount>2</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>`+
		contents("photos/a&amp;b.jpg")+
		contents("notes/&lt;draft&gt;.txt")+
		`</ListBucketResult>`)

	root := mustDecode(t, got)
	var keys []string
	for c := range root.ChildrenNamed("Contents") {
		key, _ := c.Child("Key")
		etag, _ := c.Child("ETag")
		keys = append(keys, key.Text)
		if etag.Text != `"fba9dede5f27731c9771645a39863328"` {
			t.Errorf("ETag decoded to %q", etag.Text)
		}
	}
	if strings.Join(keys, ",") != "photos/a&b.jpg,notes/<draft>.txt" {
		t.Errorf("keys decoded to %q", keys)
	}
}

// GetBucketAcl: each Grantee binds the xsi prefix and names its type with
// xsi:type (wire-rest.md 2.2).
func TestGoldenGranteeXSIType(t *testing.T) {
	t.Parallel()
	got := write(t, func(w *xmlw.Writer) {
		w.Declaration()
		w.StartNS("AccessControlPolicy", "http://s3.amazonaws.com/doc/2006-03-01/")
		w.Start("Owner")
		element(w, "ID", "75aa57f09aa0c8caeab4f8c24e99d10f8e7faeebf76c078efc7c6caea54ba06a")
		w.End()
		w.Start("AccessControlList")
		w.Start("Grant")
		w.StartXSIType("Grantee", "CanonicalUser")
		element(w, "ID", "75aa57f09aa0c8caeab4f8c24e99d10f8e7faeebf76c078efc7c6caea54ba06a")
		w.End()
		element(w, "Permission", "FULL_CONTROL")
		w.End()
		w.End()
		w.End()
	})
	assertGolden(t, got, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
		`<AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`+
		`<Owner><ID>75aa57f09aa0c8caeab4f8c24e99d10f8e7faeebf76c078efc7c6caea54ba06a</ID></Owner>`+
		`<AccessControlList><Grant>`+
		`<Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser">`+
		`<ID>75aa57f09aa0c8caeab4f8c24e99d10f8e7faeebf76c078efc7c6caea54ba06a</ID></Grantee>`+
		`<Permission>FULL_CONTROL</Permission>`+
		`</Grant></AccessControlList></AccessControlPolicy>`)

	acl, _ := mustDecode(t, got).Child("AccessControlList")
	grant, _ := acl.Child("Grant")
	grantee, _ := grant.Child("Grantee")
	if typ, ok := grantee.Attr("type"); !ok || typ != "CanonicalUser" {
		t.Errorf("Grantee type decoded to %q, %t", typ, ok)
	}
}
