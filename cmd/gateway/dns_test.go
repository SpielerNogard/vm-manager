package main

import (
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func query(t *testing.T, addr, name string, qt dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	n := dnsmessage.MustNewName(name)
	b, err := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 7, RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: n, Type: qt, Class: dnsmessage.ClassINET}}}).Pack()
	if err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write(b)
	buf := make([]byte, 1500)
	k, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	var m dnsmessage.Message
	if err := m.Unpack(buf[:k]); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDNSAnswers(t *testing.T) {
	up, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer up.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			k, from, err := up.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:k]) != nil {
				continue
			}
			m.Response = true
			m.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: m.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET},
				Body: &dnsmessage.AResource{A: [4]byte{9, 9, 9, 9}}}}
			out, _ := m.Pack()
			up.WriteTo(out, from)
		}
	}()
	conn, _ := net.ListenPacket("udp", "127.0.0.1:0")
	defer conn.Close()
	s := &dnsServer{}
	s.setRecords(map[string]string{"dev.mgmt.internal.": "10.200.0.20"})
	go s.serveUDP(conn, up.LocalAddr().String())
	addr := conn.LocalAddr().String()

	m := query(t, addr, "dev.mgmt.internal.", dnsmessage.TypeA)
	if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{10, 200, 0, 20} || !m.Authoritative {
		t.Fatalf("A: %+v", m)
	}
	m = query(t, addr, "dev.mgmt.internal.", dnsmessage.TypeAAAA)
	if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
		t.Fatalf("AAAA: %+v", m)
	}
	m = query(t, addr, "gone.mgmt.internal.", dnsmessage.TypeA)
	if m.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("gone: %+v", m)
	}
	for _, n := range []string{"mgmt.internal.", "internal."} {
		m = query(t, addr, n, dnsmessage.TypeA)
		if m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 {
			t.Fatalf("%s: %+v", n, m)
		}
	}
	s.setRecords(map[string]string{})
	m = query(t, addr, "dev.mgmt.internal.", dnsmessage.TypeA)
	if m.RCode != dnsmessage.RCodeNameError {
		t.Fatalf("after clear: %+v", m)
	}
	m = query(t, addr, "example.com.", dnsmessage.TypeA)
	if len(m.Answers) != 1 || m.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{9, 9, 9, 9} {
		t.Fatalf("forward: %+v", m)
	}
}
