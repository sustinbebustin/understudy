// Command sink is the recording end of the DynamoDB Local egress test
// (dynamodb-local-terms.md 5.3 and 7). DynamoDB Local runs in the sink's
// network namespace, on a network with no route off the host. The sink
// routes every IPv4 and IPv6 address to the namespace itself, so a
// connection to any address fails locally instead of being dropped unseen;
// answers every DNS A query with its own address; and writes one line to
// stdout, which the test reads, per DNS query and per TCP SYN or UDP
// datagram the namespace sends, whatever its address or port. Traffic the
// namespace receives, such as the test's requests to DynamoDB Local, is not
// recorded. It needs NET_ADMIN for the routes and NET_RAW for the capture.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall"
	"time"
)

// With the arguments `probe ADDR:PORT`, the command instead attempts one
// TCP connection and sends one UDP datagram to ADDR:PORT and exits: run in
// the sink's namespace, it shows the sink records an attempt to a literal
// address on an arbitrary port.
func main() {
	if len(os.Args) == 3 && os.Args[1] == "probe" {
		target, err := netip.ParseAddrPort(os.Args[2])
		if err != nil {
			log.Fatal("sink probe: ", err)
		}
		probe(context.Background(), target)
		return
	}
	log.Fatal("sink: ", run(context.Background(), os.Stdout))
}

// probe attempts a connection and sends a datagram to target. Nothing
// listens there, so the attempts fail locally; each outcome goes to stderr,
// which the test does not read.
func probe(ctx context.Context, target netip.AddrPort) {
	d := net.Dialer{Timeout: 2 * time.Second}
	if c, err := d.DialContext(ctx, "tcp", target.String()); err != nil {
		log.Print("sink probe: tcp: ", err)
	} else if err := c.Close(); err != nil {
		log.Print("sink probe: tcp: ", err)
	}
	c, err := d.DialContext(ctx, "udp", target.String())
	if err != nil {
		log.Print("sink probe: udp: ", err)
		return
	}
	if _, err := c.Write([]byte("probe")); err != nil {
		log.Print("sink probe: udp: ", err)
	}
	if err := c.Close(); err != nil {
		log.Print("sink probe: udp: ", err)
	}
}

// run serves until a socket fails, and returns that failure.
func run(ctx context.Context, out io.Writer) error {
	self, err := ownAddress()
	if err != nil {
		return err
	}
	if err := routeEverythingHere(); err != nil {
		return err
	}
	capture, err := openCapture()
	if err != nil {
		return err
	}
	var lc net.ListenConfig
	udp, err := lc.ListenPacket(ctx, "udp4", ":53")
	if err != nil {
		return err
	}
	failed := make(chan error, 2)
	go func() { failed <- record(capture, out) }()
	go func() { failed <- resolve(udp, self, out) }()
	if _, err := fmt.Fprintln(out, "ready", self); err != nil {
		return err
	}
	return <-failed
}

// resolve answers DNS queries on udp with self and records each.
func resolve(udp net.PacketConn, self net.IP, out io.Writer) error {
	buf := make([]byte, 1500)
	for {
		n, from, err := udp.ReadFrom(buf)
		if err != nil {
			return err
		}
		name, answer, ok := respond(buf[:n], self)
		if _, err := fmt.Fprintln(out, "dns", name, from); err != nil {
			return err
		}
		if ok {
			if _, err := udp.WriteTo(answer, from); err != nil {
				return err
			}
		}
	}
}

// ownAddress is the sink's IPv4 address on the test network.
func ownAddress() (net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.To4(), nil
		}
	}
	return nil, errors.New("the container has no IPv4 address")
}

// routeEverythingHere adds `local default dev lo` to the main table for
// IPv4 and, when the namespace has IPv6, for IPv6: the test network's own
// subnet keeps its more specific route, so the test still reaches DynamoDB
// Local, and every other address is this namespace.
func routeEverythingHere() error {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		return err
	}
	if err := addLocalDefault(syscall.AF_INET, lo.Index); err != nil {
		return fmt.Errorf("routing IPv4 here: %w", err)
	}
	disabled, err := os.ReadFile("/proc/sys/net/ipv6/conf/all/disable_ipv6")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(disabled)) == "1" {
		return nil
	}
	if err := addLocalDefault(syscall.AF_INET6, lo.Index); err != nil {
		return fmt.Errorf("routing IPv6 here: %w", err)
	}
	return nil
}

// addLocalDefault sends one RTM_NEWROUTE over rtnetlink, as
// `ip route replace local default dev lo table main` does.
func addLocalDefault(family uint8, oif int) error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	kernel := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return err
	}
	if oif <= 0 || oif > math.MaxInt32 {
		return fmt.Errorf("interface index %d is out of range", oif)
	}
	ne := binary.NativeEndian
	const size = syscall.NLMSG_HDRLEN + syscall.SizeofRtMsg + 8
	msg := make([]byte, size)
	ne.PutUint32(msg[0:], size)
	ne.PutUint16(msg[4:], syscall.RTM_NEWROUTE)
	ne.PutUint16(msg[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_ACK|syscall.NLM_F_CREATE|syscall.NLM_F_REPLACE)
	ne.PutUint32(msg[8:], 1)
	rt := msg[syscall.NLMSG_HDRLEN:]
	rt[0] = family
	rt[4] = syscall.RT_TABLE_MAIN
	rt[5] = syscall.RTPROT_BOOT
	rt[6] = syscall.RT_SCOPE_HOST
	rt[7] = syscall.RTN_LOCAL
	attr := rt[syscall.SizeofRtMsg:]
	ne.PutUint16(attr[0:], 8)
	ne.PutUint16(attr[2:], syscall.RTA_OIF)
	ne.PutUint32(attr[4:], uint32(oif))
	if err := syscall.Sendto(fd, msg, 0, kernel); err != nil {
		return err
	}
	ack := make([]byte, 4096)
	n, _, err := syscall.Recvfrom(fd, ack, 0)
	if err != nil {
		return err
	}
	if n < syscall.NLMSG_HDRLEN+4 || ne.Uint16(ack[4:]) != syscall.NLMSG_ERROR {
		return fmt.Errorf("rtnetlink answered %d bytes without an acknowledgement", n)
	}
	var code int32
	if err := binary.Read(bytes.NewReader(ack[syscall.NLMSG_HDRLEN:n]), ne, &code); err != nil {
		return err
	}
	switch {
	case code == 0:
		return nil
	case code < 0 && code > -4096:
		return syscall.Errno(-code)
	}
	return fmt.Errorf("rtnetlink answered error code %d", code)
}

// htons converts a 16-bit value to network byte order.
func htons(v uint16) uint16 {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return binary.NativeEndian.Uint16(b[:])
}

// openCapture opens a packet socket that sees every packet on every
// interface of the namespace, link headers stripped.
func openCapture() (int, error) {
	return syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, int(htons(syscall.ETH_P_ALL)))
}

// record writes a line for every TCP SYN and UDP datagram the namespace
// sends, except the sink's own DNS answers.
func record(fd int, out io.Writer) error {
	buf := make([]byte, 65536)
	for {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		ll, ok := from.(*syscall.SockaddrLinklayer)
		if !ok || ll.Pkttype != syscall.PACKET_OUTGOING {
			continue
		}
		if line, ok := describe(htons(ll.Protocol), buf[:n]); ok {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return err
			}
		}
	}
}

// IP protocol numbers.
const (
	protoTCP = 6
	protoUDP = 17
)

// describe returns the line for an outgoing IP packet worth recording.
func describe(ethertype uint16, p []byte) (string, bool) {
	var proto byte
	var src, dst netip.Addr
	var l4 []byte
	switch ethertype {
	case syscall.ETH_P_IP:
		if len(p) < 20 || len(p) < int(p[0]&0x0f)*4 {
			return "", false
		}
		proto = p[9]
		src, _ = netip.AddrFromSlice(p[12:16])
		dst, _ = netip.AddrFromSlice(p[16:20])
		l4 = p[int(p[0]&0x0f)*4:]
	case syscall.ETH_P_IPV6:
		if len(p) < 40 {
			return "", false
		}
		proto = p[6]
		src, _ = netip.AddrFromSlice(p[8:24])
		dst, _ = netip.AddrFromSlice(p[24:40])
		l4 = p[40:]
	default:
		return "", false
	}
	if len(l4) < 4 {
		return "", false
	}
	from := netip.AddrPortFrom(src, binary.BigEndian.Uint16(l4[0:]))
	to := netip.AddrPortFrom(dst, binary.BigEndian.Uint16(l4[2:]))
	switch proto {
	case protoTCP:
		const syn, ack = 0x02, 0x10
		if len(l4) < 14 || l4[13]&syn == 0 || l4[13]&ack != 0 {
			return "", false
		}
		return fmt.Sprintf("syn %s -> %s", from, to), true
	case protoUDP:
		if from.Port() == 53 {
			return "", false
		}
		return fmt.Sprintf("udp %s -> %s", from, to), true
	}
	return "", false
}

// respond answers a DNS query: an A query gets self, any other type an
// empty answer.
func respond(q []byte, self net.IP) (name string, answer []byte, ok bool) {
	if len(q) < 12 {
		return "?", nil, false
	}
	var labels []string
	i := 12
	for i < len(q) && q[i] != 0 {
		n := int(q[i])
		if i+1+n > len(q) {
			return "?", nil, false
		}
		labels = append(labels, string(q[i+1:i+1+n]))
		i += 1 + n
	}
	if i+5 > len(q) {
		return "?", nil, false
	}
	question := q[12 : i+5]
	qtype := binary.BigEndian.Uint16(q[i+1:])
	answer = make([]byte, 12, 12+len(question)+16)
	copy(answer, q[:2])
	binary.BigEndian.PutUint16(answer[2:], 0x8180)
	binary.BigEndian.PutUint16(answer[4:], 1)
	answer = append(answer, question...)
	if qtype == 1 {
		binary.BigEndian.PutUint16(answer[6:], 1)
		answer = append(answer, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
		answer = append(answer, self...)
	}
	return strings.Join(labels, "."), answer, true
}
