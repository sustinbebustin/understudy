package config

import (
	"crypto/x509"
	"encoding/pem"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/egress"
)

// allowHint is the grammar a refused allow-list entry is told
// (sns-http-delivery.md 7.2).
const allowHint = "entries are host:port, the host localhost, an IP literal ([v6] for IPv6) or a DNS name, and the port 1-65535 or *"

// setSNSAllow parses sns.httpDelivery.allow at load (ADR 0015 amendment),
// so an entry egress refuses, malformed or naming a host it never reaches,
// stops serve rather than being ignored (sns-http-delivery.md 7.2, 7.3).
func (d *draft) setSNSAllow(v value) *Error {
	list, err := egress.ParseAllowList(egress.Setting{Service: "sns", Feature: "httpDelivery"}, v.entries())
	if err != nil {
		return &Error{Where: v.where, Reason: "entry " + strconv.Itoa(err.Index) + ": " + err.Reason, Hint: allowHint}
	}
	d.cfg.SNS.HTTPDelivery.Allow = list
	return nil
}

// setSNSCABundle reads and parses sns.httpDelivery.caBundle through Load's
// file reader, so a bundle that is missing or holds no usable certificate
// stops serve rather than leaving https deliveries to fail one by one
// (sns-http-delivery.md 7.4).
func (d *draft) setSNSCABundle(v value) *Error {
	if v.text == "" {
		return &Error{Where: v.where, Reason: "is empty", Hint: "give a path or remove the setting"}
	}
	data, err := d.readFile(v.text)
	if err != nil {
		return &Error{Where: v.where, Reason: "cannot read the CA bundle " + quote(v.text) + ": " + err.Error(), Hint: "check the path, or remove the setting"}
	}
	certs, reason := parseCABundle(data)
	if reason != "" {
		return &Error{Where: v.where, Reason: quote(v.text) + reason, Hint: "write the CA certificates as PEM CERTIFICATE blocks, as openssl x509 -outform PEM prints them"}
	}
	d.cfg.SNS.HTTPDelivery.CABundle = certs
	return nil
}

// parseCABundle parses the PEM CERTIFICATE blocks of data, skipping blocks
// of other types; reason, empty on success, continues a sentence that
// starts with the bundle's path. Certificates are counted from 0, as
// allow-list entries are.
func parseCABundle(data []byte) (certs []*x509.Certificate, reason string) {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, ": certificate " + strconv.Itoa(len(certs)) + " does not parse: " + err.Error()
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, " holds no PEM certificate"
	}
	return certs, ""
}
