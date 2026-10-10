package awsquery

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// contentError is a body decodeContent refuses: an
// unsupportedEncodingError, an invalidGzipError or an
// inflatedTooLargeError.
type contentError interface {
	error
	isContentError()
}

// unsupportedEncodingError is a Content-Encoding other than identity or
// gzip.
type unsupportedEncodingError struct{ encoding string }

// invalidGzipError is a gzip body that does not decompress.
type invalidGzipError struct{ err error }

// inflatedTooLargeError is a gzip body that decompresses past limit bytes.
type inflatedTooLargeError struct{ limit int64 }

func (e unsupportedEncodingError) Error() string {
	return "Content-Encoding " + e.encoding + " is not supported; send the body uncompressed or gzip-compressed"
}

func (e invalidGzipError) Error() string {
	return "the request body is not valid gzip data: " + e.err.Error()
}

func (e inflatedTooLargeError) Error() string {
	return "the decompressed request body exceeds " + strconv.FormatInt(e.limit, 10) + " bytes"
}

func (unsupportedEncodingError) isContentError() {}
func (invalidGzipError) isContentError()         {}
func (inflatedTooLargeError) isContentError()    {}

// decodeContent returns body as its Content-Encoding header says to read
// it: unchanged for none or identity, decompressed for gzip, refused past
// limit bytes so that a small compressed body cannot expand without
// bound. CloudWatch PutMetricData has @requestCompression, and botocore
// gzips Query bodies of 10240 bytes or more (wire-json-query.md 3.2).
func decodeContent(h http.Header, body []byte, limit int64) ([]byte, contentError) {
	switch encoding := strings.ToLower(strings.TrimSpace(h.Get("Content-Encoding"))); encoding {
	case "", "identity":
		return body, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, invalidGzipError{err: err}
		}
		out, err := io.ReadAll(io.LimitReader(zr, limit+1))
		if err != nil {
			return nil, invalidGzipError{err: err}
		}
		if int64(len(out)) > limit {
			return nil, inflatedTooLargeError{limit: limit}
		}
		return out, nil
	default:
		return nil, unsupportedEncodingError{encoding: encoding}
	}
}
