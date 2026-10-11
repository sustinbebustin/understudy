package kms

import (
	"bytes"
	"crypto/rand"
	"maps"
	"testing"

	"pgregory.net/rapid"
)

func material(t *rapid.T) []byte {
	return rapid.SliceOfN(rapid.Byte(), materialBytes, materialBytes).Draw(t, "material")
}

func encryptionContext(t *rapid.T, label string) map[string]string {
	return rapid.MapOfN(rapid.String(), rapid.String(), 0, 4).Draw(t, label)
}

const testKeyARN = "arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab"

// A sealed blob opens with the same key material and context, names the key
// it was sealed under, and fits the 6144-byte CiphertextBlob limit for the
// largest plaintext Encrypt accepts.
func TestSealOpenRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := material(t)
		ctx := encryptionContext(t, "context")
		plain := rapid.SliceOfN(rapid.Byte(), 1, 4096).Draw(t, "plaintext")
		blob, err := seal(m, testKeyARN, ctx, plain)
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) > 6144 {
			t.Fatalf("blob is %d bytes, over the 6144-byte limit", len(blob))
		}
		env, ok := parseBlob(blob)
		if !ok {
			t.Fatal("parseBlob refused a sealed blob")
		}
		if env.keyARN != testKeyARN {
			t.Fatalf("key = %q, want %q", env.keyARN, testKeyARN)
		}
		got, ok := env.open(m, maps.Clone(ctx))
		if !ok || !bytes.Equal(got, plain) {
			t.Fatalf("open = %x, %v; want %x", got, ok, plain)
		}
	})
}

// Any other encryption context fails to open: AWS answers
// InvalidCiphertextException when the context is not an exact match.
func TestOpenRefusesAnotherContext(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := material(t)
		ctx := encryptionContext(t, "context")
		other := encryptionContext(t, "other")
		if maps.Equal(ctx, other) {
			t.Skip("same context")
		}
		blob, err := seal(m, testKeyARN, ctx, []byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		env, ok := parseBlob(blob)
		if !ok {
			t.Fatal("parseBlob refused a sealed blob")
		}
		if _, ok := env.open(m, other); ok {
			t.Fatalf("opened with context %v, sealed with %v", other, ctx)
		}
	})
}

// Flipping any bit of a blob makes it unparseable or unopenable, so a
// corrupted ciphertext is InvalidCiphertextException and never decrypts to
// other bytes.
func TestTamperedBlobNeverOpens(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := material(t)
		blob, err := seal(m, testKeyARN, nil, []byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		i := rapid.IntRange(0, len(blob)-1).Draw(t, "byte")
		bit := rapid.IntRange(0, 7).Draw(t, "bit")
		blob[i] ^= 1 << bit
		env, ok := parseBlob(blob)
		if !ok {
			return
		}
		if _, ok := env.open(m, nil); ok {
			t.Fatalf("a blob with byte %d bit %d flipped still opened", i, bit)
		}
	})
}

// Arbitrary bytes never parse into a blob that opens.
func TestArbitraryBytesNeverOpen(t *testing.T) {
	m := make([]byte, materialBytes)
	if _, err := rand.Read(m); err != nil {
		t.Fatal(err)
	}
	rapid.Check(t, func(t *rapid.T) {
		b := rapid.SliceOfN(rapid.Byte(), 0, 200).Draw(t, "bytes")
		if env, ok := parseBlob(b); ok {
			if _, ok := env.open(m, nil); ok {
				t.Fatalf("%x opened", b)
			}
		}
	})
}

// The context encoding is injective, so two different contexts are never
// the same additional authenticated data.
func TestContextEncodingIsInjective(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := encryptionContext(t, "a")
		b := encryptionContext(t, "b")
		if maps.Equal(a, b) != bytes.Equal(encodeContext(a), encodeContext(b)) {
			t.Fatalf("contexts %v and %v: equal maps %v, equal encodings %v", a, b, maps.Equal(a, b), bytes.Equal(encodeContext(a), encodeContext(b)))
		}
	})
}
