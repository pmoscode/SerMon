package geoip_test

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"sort"
	"strings"
)

// A minimal writer of MaxMind DB files (https://maxmind.github.io/MaxMind-DB/) for the tests: an
// IPv6 tree with 24-bit records, IPv4 networks below ::/96, and records {country: {iso_code}}
// ("registered:XX": {registered_country: {iso_code}}).
// It keeps the tests free of a writer dependency.

type entry struct {
	key   string
	value any // string, uint16, uint32, uint64, []string, map[string]any (as []entry), []entry
}

func control(buf *bytes.Buffer, typ byte, size int) {
	var head byte
	var extra []byte
	switch {
	case size < 29:
		head = byte(size)
	case size < 29+256:
		head, extra = 29, []byte{byte(size - 29)}
	default:
		head, extra = 30, binary.BigEndian.AppendUint16(nil, uint16(size-285))
	}
	if typ <= 7 {
		buf.WriteByte(typ<<5 | head)
	} else {
		buf.WriteByte(head)
		buf.WriteByte(typ - 7)
	}
	buf.Write(extra)
}

func uintBytes(v uint64) []byte {
	b := binary.BigEndian.AppendUint64(nil, v)
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	return b
}

func encode(buf *bytes.Buffer, v any) {
	switch v := v.(type) {
	case string:
		control(buf, 2, len(v))
		buf.WriteString(v)
	case uint16:
		b := uintBytes(uint64(v))
		control(buf, 5, len(b))
		buf.Write(b)
	case uint32:
		b := uintBytes(uint64(v))
		control(buf, 6, len(b))
		buf.Write(b)
	case uint64:
		b := uintBytes(v)
		control(buf, 9, len(b))
		buf.Write(b)
	case []string:
		control(buf, 11, len(v))
		for _, s := range v {
			encode(buf, s)
		}
	case []entry:
		control(buf, 7, len(v))
		for _, e := range v {
			encode(buf, e.key)
			encode(buf, e.value)
		}
	default:
		panic("mmdb: unsupported value")
	}
}

type trieNode struct {
	child [2]*trieNode
	data  [2]int // index+1 into the data records, 0: none
}

// mmdb builds a database of the given type mapping networks to country codes.
func mmdb(dbType, description string, networks map[string]string) []byte {
	root := &trieNode{}
	var records [][]byte
	prefixes := make([]string, 0, len(networks))
	for p := range networks {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	for _, raw := range prefixes {
		p := netip.MustParsePrefix(raw).Masked()
		addr, bits := p.Addr(), p.Bits()
		if addr.Is4() {
			addr, bits = netip.AddrFrom16(addr.As16()), bits+96
			a := addr.As16()
			a[10], a[11] = 0, 0 // ::a.b.c.d, not ::ffff:a.b.c.d
			addr = netip.AddrFrom16(a)
		}
		var rec bytes.Buffer
		key, code := "country", networks[raw]
		if c, ok := strings.CutPrefix(code, "registered:"); ok {
			key, code = "registered_country", c
		}
		encode(&rec, []entry{{key, []entry{{"iso_code", code}}}})
		records = append(records, rec.Bytes())
		ip := addr.As16()
		n := root
		for i := range bits {
			bit := (ip[i/8] >> (7 - i%8)) & 1
			if i == bits-1 {
				n.data[bit] = len(records)
				break
			}
			if n.child[bit] == nil {
				n.child[bit] = &trieNode{}
			}
			n = n.child[bit]
		}
	}
	// Number the nodes breadth first, then lay out the data section.
	var nodes []*trieNode
	index := map[*trieNode]int{}
	for queue := []*trieNode{root}; len(queue) > 0; queue = queue[1:] {
		n := queue[0]
		index[n] = len(nodes)
		nodes = append(nodes, n)
		for _, c := range n.child {
			if c != nil {
				queue = append(queue, c)
			}
		}
	}
	var data bytes.Buffer
	offsets := make([]int, len(records))
	for i, r := range records {
		offsets[i] = data.Len()
		data.Write(r)
	}
	count := len(nodes)
	var out bytes.Buffer
	put := func(v int) { out.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)}) }
	for _, n := range nodes {
		for bit := range 2 {
			switch {
			case n.child[bit] != nil:
				put(index[n.child[bit]])
			case n.data[bit] > 0:
				put(count + 16 + offsets[n.data[bit]-1])
			default:
				put(count)
			}
		}
	}
	out.Write(make([]byte, 16))
	out.Write(data.Bytes())
	out.WriteString("\xAB\xCD\xEFMaxMind.com")
	encode(&out, []entry{
		{"binary_format_major_version", uint16(2)},
		{"binary_format_minor_version", uint16(0)},
		{"build_epoch", uint64(1_764_000_000)},
		{"database_type", dbType},
		{"description", []entry{{"en", description}}},
		{"ip_version", uint16(6)},
		{"languages", []string{"en"}},
		{"node_count", uint32(count)},
		{"record_size", uint16(24)},
	})
	return out.Bytes()
}
