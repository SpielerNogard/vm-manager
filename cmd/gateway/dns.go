package main

import (
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type dnsServer struct {
	mu      sync.RWMutex
	records map[string]string
}

func (d *dnsServer) setRecords(r map[string]string) {
	d.mu.Lock()
	d.records = r
	d.mu.Unlock()
}

func (d *dnsServer) lookup(name string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ip, ok := d.records[name]
	return ip, ok
}

func (d *dnsServer) handle(network string, req []byte, upstream string) ([]byte, error) {
	var m dnsmessage.Message
	if err := m.Unpack(req); err != nil {
		return nil, err
	}
	if len(m.Questions) != 1 {
		return reply(m, dnsmessage.RCodeFormatError, nil, false)
	}
	q := m.Questions[0]
	name := strings.ToLower(q.Name.String())
	if name != "internal." && !strings.HasSuffix(name, ".internal.") {
		return forward(network, req, upstream)
	}
	ip, ok := d.lookup(name)
	if !ok {
		return reply(m, dnsmessage.RCodeNameError, nil, true)
	}
	var answers []dnsmessage.Resource
	if addr := net.ParseIP(ip).To4(); q.Type == dnsmessage.TypeA && addr != nil {
		answers = append(answers, dnsmessage.Resource{
			Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 5},
			Body:   &dnsmessage.AResource{A: [4]byte(addr)},
		})
	}
	return reply(m, dnsmessage.RCodeSuccess, answers, true)
}

func reply(req dnsmessage.Message, rcode dnsmessage.RCode, answers []dnsmessage.Resource, authoritative bool) ([]byte, error) {
	return (&dnsmessage.Message{
		Header: dnsmessage.Header{ID: req.ID, Response: true, Authoritative: authoritative,
			RecursionDesired: req.RecursionDesired, RCode: rcode},
		Questions: req.Questions,
		Answers:   answers,
	}).Pack()
}

func forward(network string, req []byte, upstream string) ([]byte, error) {
	c, err := net.DialTimeout(network, upstream, 3*time.Second)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if network == "tcp" {
		return exchangeTCP(c, req)
	}
	if _, err := c.Write(req); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := c.Read(buf)
	return buf[:n], err
}

func exchangeTCP(c net.Conn, req []byte) ([]byte, error) {
	if err := writeFramed(c, req); err != nil {
		return nil, err
	}
	return readFramed(c)
}

func writeFramed(w io.Writer, b []byte) error {
	_, err := w.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(b))), b...))
	return err
}

func readFramed(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(l[:]))
	_, err := io.ReadFull(r, b)
	return b, err
}

func (d *dnsServer) serveUDP(conn net.PacketConn, upstream string) {
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		req := append([]byte(nil), buf[:n]...)
		go func() {
			if resp, err := d.handle("udp", req, upstream); err == nil {
				conn.WriteTo(resp, from)
			}
		}()
	}
}

func (d *dnsServer) serveTCP(l net.Listener, upstream string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			for {
				c.SetDeadline(time.Now().Add(10 * time.Second))
				req, err := readFramed(c)
				if err != nil {
					return
				}
				resp, err := d.handle("tcp", req, upstream)
				if err != nil || writeFramed(c, resp) != nil {
					return
				}
			}
		}()
	}
}
