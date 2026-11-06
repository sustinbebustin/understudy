// Package egress is the only package that dials outbound HTTP: deliveries
// to subscriber endpoints and nothing else (ADR 0015 amendment). An
// emulator that POSTs to caller-chosen URLs is a request-forgery primitive
// (sns-http-delivery.md 7.1), so every request passes, in order, a scheme
// check, the hard refusals of AWS names and forbidden addresses (7.3), a
// producer's allow-list in the one key shape <service>.<feature>.allow
// (7.2), and address checks after resolution, on the address actually
// dialed (7.4). The client uses no proxy, never follows a redirect, speaks
// HTTP/1.1 only and bounds the request by a wall-clock deadline from the
// emulator clock (ADR 0006).
//
// Name resolution and dialing go through the Resolver and Dialer seams, so
// tests run against in-memory listeners under testing/synctest.
package egress
