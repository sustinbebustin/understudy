package httpevent

import "errors"

// V1Front is the front end whose payload format 1.0 event EventV1 builds:
// the fronts differ in header case, which value of a repeated header or
// query parameter the single-value maps keep, and the path they report.
//
//sumtype:decl
type V1Front interface{ isV1Front() }

// HTTPAPI is an API Gateway HTTP API (apigateway-http.md 5.2): header
// names lowercased, the last value of a repeated header and the first of
// a repeated query parameter in the single-value maps, and the path as
// sent.
type HTTPAPI struct{}

func (HTTPAPI) isV1Front() {}

// errV1NotImplemented is the answer of the payload format 1.0 functions,
// which are not implemented yet.
var errV1NotImplemented = errors.New("httpevent: payload format 1.0 is not implemented yet")

// EventV1 builds the payload format version 1.0 event for r as front f
// reports it (apigateway-http.md 5.2). It is not implemented yet and
// returns an error.
func EventV1(_ Request, _ Context, _ V1Front) ([]byte, error) {
	return nil, errV1NotImplemented
}

// InferV1 turns a function's output into the HTTP response under payload
// format 1.0 (apigateway-http.md 5.4). It is not implemented yet and
// returns an error.
func InferV1(_ []byte) (Response, error) {
	return Response{}, errV1NotImplemented
}
