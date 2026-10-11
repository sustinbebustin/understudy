package kms

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"maps"
	"slices"
)

// materialBytes is the size of a key's AES-256 material.
const materialBytes = 32

// blobVersion is the first byte of every ciphertext blob understudy writes.
const blobVersion = 1

// The ciphertext blob is understudy's own self-describing envelope; AWS's is
// undocumented and no client parses it (iam-sts-kms.md 5.7):
//
//	version (1 byte) | n (1 byte) | key ARN (n bytes) | nonce (12 bytes) | AES-256-GCM ciphertext and tag
//
// The additional authenticated data is the header (version, n and the key
// ARN) followed by encodeContext of the encryption context, so neither the
// key a blob names nor its context can be changed without Decrypt failing.

// envelope is a parsed ciphertext blob.
type envelope struct {
	keyARN string
	header []byte
	nonce  []byte
	sealed []byte
}

// seal encrypts plaintext under material with the encryption context as
// additional authenticated data and returns the blob naming keyARN.
func seal(material []byte, keyARN string, context map[string]string, plaintext []byte) ([]byte, error) {
	n := len(keyARN)
	if n > 255 {
		return nil, &KeyARNLengthError{ARN: keyARN}
	}
	aead, err := newAEAD(material)
	if err != nil {
		return nil, err
	}
	header := append([]byte{blobVersion, byte(n)}, keyARN...)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	blob := append(slices.Clip(header), nonce...)
	return aead.Seal(blob, nonce, plaintext, aad(header, context)), nil
}

// KeyARNLengthError is a key ARN longer than the 255 bytes a blob header
// holds; KMS key ARNs are under 100.
type KeyARNLengthError struct {
	ARN string
}

func (e *KeyARNLengthError) Error() string {
	return "kms: cannot seal under key " + e.ARN + ": a ciphertext blob holds a key ARN of at most 255 bytes"
}

// parseBlob splits a blob into its parts; false means the bytes are not a
// blob understudy wrote.
func parseBlob(blob []byte) (envelope, bool) {
	if len(blob) < 2 || blob[0] != blobVersion {
		return envelope{}, false
	}
	end := 2 + int(blob[1])
	if len(blob) < end+12+16 {
		return envelope{}, false
	}
	return envelope{
		keyARN: string(blob[2:end]),
		header: blob[:end],
		nonce:  blob[end : end+12],
		sealed: blob[end+12:],
	}, true
}

// open decrypts the envelope with material under context; false means the
// material, the context or the bytes do not match.
func (e envelope) open(material []byte, context map[string]string) ([]byte, bool) {
	aead, err := newAEAD(material)
	if err != nil {
		return nil, false
	}
	plain, err := aead.Open(nil, e.nonce, e.sealed, aad(e.header, context))
	return plain, err == nil
}

func newAEAD(material []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(material)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func aad(header []byte, context map[string]string) []byte {
	return append(slices.Clip(header), encodeContext(context)...)
}

// encodeContext is the canonical form of an encryption context: its pairs
// sorted by key, each key and value prefixed by its length, so equal
// contexts encode equally and different ones differently.
func encodeContext(context map[string]string) []byte {
	var b []byte
	b = binary.AppendUvarint(b, uint64(len(context)))
	for _, k := range slices.Sorted(maps.Keys(context)) {
		b = binary.AppendUvarint(b, uint64(len(k)))
		b = append(b, k...)
		b = binary.AppendUvarint(b, uint64(len(context[k])))
		b = append(b, context[k]...)
	}
	return b
}
