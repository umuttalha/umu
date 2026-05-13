// dns-local: a minimal DNS responder that serves /etc/hosts entries.
// Listens on 127.0.0.1:53. Quickwit's async Rust resolver can reach this.
package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"strings"
)

var hosts = map[string][]net.IP{}

func main() {
	log.SetOutput(os.Stdout)
	loadHosts()

	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal("dns-local: failed to listen:", err)
	}
	defer conn.Close()
	log.Println("[dns-local] listening on", addr)

	buf := make([]byte, 512)
	for {
		n, clientAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		go handleDNS(conn, clientAddr, buf[:n], n)
	}
}

func loadHosts() {
	f, err := os.Open("/etc/hosts")
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		ip := net.ParseIP(fields[0])
		if ip == nil {
			continue
		}
		for _, host := range fields[1:] {
			host = strings.ToLower(strings.TrimSuffix(host, "."))
			hosts[host] = append(hosts[host], ip)
		}
	}
	log.Printf("[dns-local] loaded %d host entries", len(hosts))
}

func handleDNS(conn *net.UDPConn, clientAddr *net.UDPAddr, query []byte, pktLen int) {
	if len(query) < 12 {
		return
	}

	qname, _, _ := readName(query, 12)
	log.Printf("[dns-local] query: %s (%d bytes)", qname, pktLen)

	resp := make([]byte, 512)
	copy(resp, query[:2])                               // transaction ID
	resp[2] = query[2] | 0x80                            // QR=1 (response)
	resp[3] = query[3]                                   // RD copy
	resp[4] = query[4]                                   // QDCOUNT high
	resp[5] = query[5]                                   // QDCOUNT low
	// ANCOUNT, NSCOUNT, ARCOUNT set below

	// Extract question name
	qname, qtype, off := readName(query, 12)
	if off == -1 {
		return
	}

	// Look up in hosts
	qnameLower := strings.ToLower(strings.TrimSuffix(qname, "."))
	ips, ok := hosts[qnameLower]
	if !ok {
		for host, hostIPs := range hosts {
			if strings.HasSuffix(qnameLower, "."+host) {
				ips = hostIPs
				ok = true
				break
			}
		}
	}

	var answers []byte
	var answerCount int

	if ok {
		for _, ip := range ips {
			isV4 := ip.To4() != nil
			if qtype == 1 && isV4 {
				answers = append(answers, 0xc0, 0x0c)
				answers = append(answers, 0x00, 0x01)
				answers = append(answers, 0x00, 0x01)
				answers = append(answers, 0x00, 0x00, 0x00, 0x3c)
				answers = append(answers, 0x00, 0x04)
				answers = append(answers, ip.To4()...)
				answerCount++
			} else if qtype == 28 {
				if !isV4 {
					answers = append(answers, 0xc0, 0x0c)
					answers = append(answers, 0x00, 0x1c)
					answers = append(answers, 0x00, 0x01)
					answers = append(answers, 0x00, 0x00, 0x00, 0x3c)
					answers = append(answers, 0x00, 0x10)
					answers = append(answers, ip.To16()...)
					answerCount++
				} else {
					mapped := make([]byte, 16)
					v4 := ip.To4()
					mapped[10] = 0xff
					mapped[11] = 0xff
					copy(mapped[12:], v4)
					answers = append(answers, 0xc0, 0x0c)
					answers = append(answers, 0x00, 0x1c)
					answers = append(answers, 0x00, 0x01)
					answers = append(answers, 0x00, 0x00, 0x00, 0x3c)
					answers = append(answers, 0x00, 0x10)
					answers = append(answers, mapped...)
					answerCount++
				}
			}
		}
	}

	resp[3] = query[3] | 0x80
	headerLen := off
	resp[6] = byte(answerCount >> 8)
	resp[7] = byte(answerCount)
	resp[8] = 0
	resp[9] = 0
	resp[10] = 0
	resp[11] = 0

	resp = append(resp[:headerLen], answers...)
	if answerCount > 0 {
		log.Printf("[dns-local] resolved %s -> %v (qtype=%d)", qname, ips, qtype)
	} else if ok {
		log.Printf("[dns-local] empty answer for %s (qtype=%d, domain known)", qname, qtype)
	} else {
		resp[3] |= 0x03
		log.Printf("[dns-local] NXDOMAIN for %s (qtype=%d)", qname, qtype)
	}
	conn.WriteToUDP(resp, clientAddr)
}

func readName(data []byte, off int) (string, uint16, int) {
	var parts []string
	origOff := off
	for {
		if off >= len(data) {
			return "", 0, -1
		}
		length := int(data[off])
		if length == 0 {
			off++
			break
		}
		if length&0xc0 == 0xc0 {
			off += 2
			break
		}
		off++
		parts = append(parts, string(data[off:off+length]))
		off += length
	}
	if off+2 > len(data) {
		return "", 0, -1
	}
	qtype := uint16(data[off])<<8 | uint16(data[off+1])
	off += 4 // skip QTYPE + QCLASS
	return strings.Join(parts, "."), qtype, origOff + (off - origOff)
}
