// Package mo builds mobile-originated messages for the MO generator: it
// picks a source MSISDN and destination, renders a weighted text template,
// encodes it (GSM 03.38 unpacked, or UCS2) and splits long texts into
// concatenated parts with a UDH.
package mo

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

const (
	CodingGSM7 byte = 0x00 // SMSC default alphabet, one septet per octet
	CodingUCS2 byte = 0x08

	gsmSingle  = 160 // septets in one message
	gsmPart    = 153 // septets per part after a 6-octet UDH
	ucs2Single = 140 // octets
	ucs2Part   = 134

	ESMClassUDHI byte = 0x40
)

// Template is a text with a relative weight. Placeholders: {seq} (message
// number in the run), {rand:N} (N random digits), {keyword} (a random
// keyword) and {time} (HH:MM:SS UTC).
type Template struct {
	Text   string
	Weight int
}

// Sources picks source MSISDNs: from a list, a numeric range, or a prefix
// followed by random digits.
type Sources struct {
	Kind   string // list | range | prefix
	List   []string
	From   string
	To     string
	Prefix string
	Digits int
}

type Config struct {
	Sources      Sources
	Destinations []string
	RandomDest   bool // false: round-robin
	Templates    []Template
	Keywords     []string
	UCS2         bool
	LongPct      int // share of messages made long (2-3 parts)
}

// Message is one MO: its text and the octets of each part to send.
type Message struct {
	Src, Dst string
	Text     string
	Coding   byte
	ESMClass byte // ESMClassUDHI when split
	Parts    [][]byte
}

// Builder renders messages from a Config. Safe for use by one goroutine.
type Builder struct {
	cfg      Config
	total    int // template weight sum
	from, to uint64
	next     atomic.Uint64 // round-robin destination
	ref      byte          // concatenation reference
}

func NewBuilder(cfg Config) (*Builder, error) {
	b := &Builder{cfg: cfg, ref: byte(rand.IntN(256))}
	if len(cfg.Destinations) == 0 {
		return nil, errors.New("no destinations")
	}
	if len(cfg.Templates) == 0 {
		return nil, errors.New("no templates")
	}
	for _, t := range cfg.Templates {
		if t.Weight <= 0 {
			return nil, fmt.Errorf("template %q: weight must be positive", t.Text)
		}
		b.total += t.Weight
	}
	switch cfg.Sources.Kind {
	case "list":
		if len(cfg.Sources.List) == 0 {
			return nil, errors.New("no source numbers")
		}
	case "range":
		from, err1 := strconv.ParseUint(cfg.Sources.From, 10, 64)
		to, err2 := strconv.ParseUint(cfg.Sources.To, 10, 64)
		if err1 != nil || err2 != nil || from > to || len(cfg.Sources.To) > 19 {
			return nil, fmt.Errorf("invalid source range %s-%s", cfg.Sources.From, cfg.Sources.To)
		}
		b.from, b.to = from, to
	case "prefix":
		if cfg.Sources.Digits < 0 || len(cfg.Sources.Prefix)+cfg.Sources.Digits == 0 {
			return nil, errors.New("invalid source prefix")
		}
	default:
		return nil, fmt.Errorf("unknown source kind %q", cfg.Sources.Kind)
	}
	return b, nil
}

// NewEncoder returns a Builder for Build only (no sources, destinations or
// templates), e.g. for a single test MO.
func NewEncoder(ucs2 bool) *Builder {
	return &Builder{cfg: Config{UCS2: ucs2}, ref: byte(rand.IntN(256))}
}

// Next builds message number seq (1-based) of a run.
func (b *Builder) Next(seq int64, now time.Time) Message {
	text := b.render(b.template(), seq, now)
	if b.cfg.LongPct > 0 && rand.IntN(100) < b.cfg.LongPct {
		text = b.lengthen(text, 2+rand.IntN(2))
	}
	return b.Build(b.source(), b.destination(), text)
}

// Build encodes text from src to dst, split if it does not fit one message.
func (b *Builder) Build(src, dst, text string) Message {
	m := Message{Src: src, Dst: dst, Text: text}
	if b.cfg.UCS2 {
		m.Coding = CodingUCS2
		m.Parts = b.split(EncodeUCS2(text), ucs2Single, ucs2Part, splitUCS2)
	} else {
		m.Coding = CodingGSM7
		m.Parts = b.split(EncodeGSM7(text), gsmSingle, gsmPart, splitGSM7)
	}
	if len(m.Parts) > 1 {
		m.ESMClass = ESMClassUDHI
	}
	return m
}

func (b *Builder) template() string {
	n := rand.IntN(b.total)
	for _, t := range b.cfg.Templates {
		if n < t.Weight {
			return t.Text
		}
		n -= t.Weight
	}
	return b.cfg.Templates[0].Text
}

func (b *Builder) render(tpl string, seq int64, now time.Time) string {
	var sb strings.Builder
	for {
		i := strings.IndexByte(tpl, '{')
		if i < 0 {
			sb.WriteString(tpl)
			return sb.String()
		}
		j := strings.IndexByte(tpl[i:], '}')
		if j < 0 {
			sb.WriteString(tpl)
			return sb.String()
		}
		sb.WriteString(tpl[:i])
		sb.WriteString(b.expand(tpl[i+1:i+j], tpl[i:i+j+1], seq, now))
		tpl = tpl[i+j+1:]
	}
}

func (b *Builder) expand(name, raw string, seq int64, now time.Time) string {
	switch {
	case name == "seq":
		return strconv.FormatInt(seq, 10)
	case name == "time":
		return now.UTC().Format("15:04:05")
	case name == "keyword":
		if len(b.cfg.Keywords) == 0 {
			return ""
		}
		return b.cfg.Keywords[rand.IntN(len(b.cfg.Keywords))]
	case strings.HasPrefix(name, "rand:"):
		n, err := strconv.Atoi(name[5:])
		if err != nil || n < 1 || n > 32 {
			return raw
		}
		return randomDigits(n)
	}
	return raw // unknown placeholder: left as typed
}

// lengthen repeats the rendered text until it needs exactly the given number
// of parts.
func (b *Builder) lengthen(text string, parts int) string {
	single, perPart, size := gsmSingle, gsmPart, func(s string) int { return len(EncodeGSM7(s)) }
	if b.cfg.UCS2 {
		single, perPart, size = ucs2Single, ucs2Part, func(s string) int { return len(EncodeUCS2(s)) }
	}
	if strings.TrimSpace(text) == "" {
		text = "MO"
	}
	long := text
	for size(long) <= max(single, perPart*(parts-1)) {
		long += " " + text
	}
	r := []rune(long)
	for size(string(r)) > perPart*parts {
		r = r[:len(r)-1]
	}
	return string(r)
}

func (b *Builder) source() string {
	s := b.cfg.Sources
	switch s.Kind {
	case "list":
		return s.List[rand.IntN(len(s.List))]
	case "range":
		v := strconv.FormatUint(b.from+rand.Uint64N(b.to-b.from+1), 10)
		if pad := len(s.From) - len(v); pad > 0 { // keep leading zeros
			v = strings.Repeat("0", pad) + v
		}
		return v
	}
	return s.Prefix + randomDigits(s.Digits)
}

func (b *Builder) destination() string {
	d := b.cfg.Destinations
	if b.cfg.RandomDest {
		return d[rand.IntN(len(d))]
	}
	return d[(b.next.Add(1)-1)%uint64(len(d))]
}

// split cuts data into parts with a concatenation UDH when it does not fit
// one message; cut keeps an escape sequence or surrogate pair together.
func (b *Builder) split(data []byte, single, perPart int, cut func([]byte, int) int) [][]byte {
	if len(data) <= single {
		return [][]byte{data}
	}
	var chunks [][]byte
	for len(data) > 0 {
		n := cut(data, perPart)
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	if len(chunks) > 255 {
		chunks = chunks[:255]
	}
	b.ref++
	parts := make([][]byte, len(chunks))
	for i, c := range chunks {
		p := make([]byte, 0, 6+len(c))
		p = append(p, 0x05, 0x00, 0x03, b.ref, byte(len(chunks)), byte(i+1))
		parts[i] = append(p, c...)
	}
	return parts
}

func splitGSM7(data []byte, max int) int {
	if len(data) <= max {
		return len(data)
	}
	if data[max-1] == gsmEscape {
		return max - 1
	}
	return max
}

func splitUCS2(data []byte, max int) int {
	if len(data) <= max {
		return len(data)
	}
	if hi := uint16(data[max-2])<<8 | uint16(data[max-1]); hi >= 0xD800 && hi <= 0xDBFF {
		return max - 2
	}
	return max
}

func randomDigits(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('0' + rand.IntN(10))
	}
	return string(b)
}

// EncodeUCS2 encodes text as UTF-16BE.
func EncodeUCS2(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, 2*len(units))
	for _, u := range units {
		out = append(out, byte(u>>8), byte(u))
	}
	return out
}
