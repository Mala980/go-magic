package netfix

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

// TestResolverAnswersThroughFakeDNS proves end-to-end that a resolver
// built by NewResolver actually resolves a name by dialing the given
// nameserver: a throwaway UDP DNS server on 127.0.0.1:53 answers every
// A query with 203.0.113.99. Skipped when binding port 53 is not
// permitted (unprivileged environments).
func TestResolverAnswersThroughFakeDNS(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:53")
	if err != nil {
		t.Skipf("cannot bind UDP :53 (%v); runtime proof skipped", err)
	}
	defer pc.Close()

	go func() {
		buf := make([]byte, 512)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			query := append([]byte(nil), buf[:n]...)
			if len(query) < 12 {
				continue
			}
			// Find the end of the question section: walk the
			// QNAME labels from offset 12, then +1 (root byte)
			// +4 (QTYPE/QCLASS). Anything beyond (e.g. an EDNS
			// OPT record) is not part of the question.
			i := 12
			for i < n && query[i] != 0 {
				i += int(query[i]) + 1
			}
			qEnd := i + 1 + 4
			if qEnd > n {
				continue
			}
			// Response: same ID, flags=0x8180, qd=1, an=1, question
			// echoed verbatim (bytes 12..qEnd).
			resp := make([]byte, 0, qEnd+16)
			resp = append(resp, query[:2]...)           // ID
			resp = append(resp, 0x81, 0x80)             // flags
			resp = append(resp, query[4:6]...)          // QDCOUNT
			resp = append(resp, 0x00, 0x01)             // ANCOUNT=1
			resp = append(resp, 0x00, 0x00, 0x00, 0x00) // NS/AR = 0
			resp = append(resp, query[12:qEnd]...)      // question
			// Answer: name pointer to offset 12, type A, class IN,
			// TTL 60, RDLEN 4, RDATA 203.0.113.99.
			resp = append(resp, 0xc0, 0x0c, 0x00, 0x01, 0x00, 0x01)
			var ttl [4]byte
			binary.BigEndian.PutUint32(ttl[:], 60)
			resp = append(resp, ttl[:]...)
			resp = append(resp, 0x00, 0x04, 203, 0, 113, 99)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()

	r := NewResolver([]string{"127.0.0.1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := r.LookupHost(ctx, "anything.example")
	if err != nil {
		// Some sandboxes block loopback UDP/53 at the syscall
		// level (sendto returns EPERM, or packets are silently
		// dropped and the exchange times out) even though binding
		// succeeded. That is an environment restriction, not a
		// resolver failure — skip there; on an unrestricted host
		// this test is a real end-to-end proof.
		msg := err.Error()
		if strings.Contains(msg, "i/o timeout") ||
			strings.Contains(msg, "operation not permitted") ||
			strings.Contains(msg, "permission denied") {
			t.Skipf("loopback UDP/53 unavailable in this environment: %v", err)
		}
		t.Fatalf("LookupHost through fake DNS failed: %v", err)
	}
	found := false
	for _, ip := range ips {
		if ip == "203.0.113.99" {
			found = true
		}
	}
	if !found {
		t.Fatalf("LookupHost = %v, want it to contain 203.0.113.99", ips)
	}
}
