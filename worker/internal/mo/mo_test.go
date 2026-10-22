package mo

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestGSM7RoundTrip(t *testing.T) {
	in := "Hello @ £5 {€} [x] ~^|\\ Ñandü"
	enc := EncodeGSM7(in)
	if got := DecodeGSM7(enc); got != in {
		t.Fatalf("round trip %q -> %q", in, got)
	}
	if !bytes.Equal(EncodeGSM7("€"), []byte{0x1B, 0x65}) || !bytes.Equal(EncodeGSM7("@A"), []byte{0x00, 0x41}) {
		t.Fatal("septet values")
	}
	if got := EncodeGSM7("हि"); string(got) != "??" {
		t.Fatalf("unmappable -> %q", got)
	}
}

func TestUCS2(t *testing.T) {
	enc := EncodeUCS2("A😀")
	if !bytes.Equal(enc, []byte{0x00, 0x41, 0xD8, 0x3D, 0xDE, 0x00}) {
		t.Fatalf("% x", enc)
	}
}

func build(t *testing.T, cfg Config) *Builder {
	t.Helper()
	if cfg.Sources.Kind == "" {
		cfg.Sources = Sources{Kind: "list", List: []string{"919800000001"}}
	}
	if cfg.Destinations == nil {
		cfg.Destinations = []string{"56161"}
	}
	if cfg.Templates == nil {
		cfg.Templates = []Template{{Text: "HI", Weight: 1}}
	}
	b, err := NewBuilder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSplitting(t *testing.T) {
	b := build(t, Config{})
	if m := b.Build("1", "2", strings.Repeat("a", 160)); len(m.Parts) != 1 || m.ESMClass != 0 {
		t.Fatalf("160 GSM chars: %d parts", len(m.Parts))
	}
	m := b.Build("1", "2", strings.Repeat("a", 161))
	if len(m.Parts) != 2 || m.ESMClass != ESMClassUDHI || len(m.Parts[0]) != 6+153 || len(m.Parts[1]) != 6+8 {
		t.Fatalf("161 GSM chars: %d parts, sizes %d/%d", len(m.Parts), len(m.Parts[0]), len(m.Parts[len(m.Parts)-1]))
	}
	udh0, udh1 := m.Parts[0][:6], m.Parts[1][:6]
	if udh0[0] != 5 || udh0[1] != 0 || udh0[2] != 3 || udh0[3] != udh1[3] || udh0[4] != 2 || udh0[5] != 1 || udh1[5] != 2 {
		t.Fatalf("UDH % x / % x", udh0, udh1)
	}
	// An escape sequence is never split across parts.
	m = b.Build("1", "2", strings.Repeat("a", 152)+"€"+strings.Repeat("a", 10))
	if p := m.Parts[0]; p[len(p)-1] == gsmEscape {
		t.Fatal("part ends with an escape septet")
	}

	u := build(t, Config{UCS2: true})
	if m := u.Build("1", "2", strings.Repeat("ह", 70)); len(m.Parts) != 1 || m.Coding != CodingUCS2 {
		t.Fatalf("70 UCS2 chars: %d parts", len(m.Parts))
	}
	// A surrogate pair is never split across parts.
	m = u.Build("1", "2", strings.Repeat("a", 66)+"😀"+strings.Repeat("a", 10))
	if p := m.Parts[0]; utf16.IsSurrogate(rune(uint16(p[len(p)-2])<<8|uint16(p[len(p)-1]))) && p[len(p)-2] < 0xDC {
		t.Fatal("part ends with a high surrogate")
	}
}

func TestTemplatesAndPlaceholders(t *testing.T) {
	b := build(t, Config{
		Templates: []Template{{Text: "#{seq} {keyword} code {rand:6} at {time} {nope}", Weight: 1}},
		Keywords:  []string{"STOP"},
	})
	now := time.Date(2026, 10, 22, 9, 8, 7, 0, time.UTC)
	m := b.Next(42, now)
	if !regexp.MustCompile(`^#42 STOP code \d{6} at 09:08:07 \{nope\}$`).MatchString(m.Text) {
		t.Fatalf("rendered %q", m.Text)
	}

	w := build(t, Config{Templates: []Template{{Text: "A", Weight: 9}, {Text: "B", Weight: 1}}})
	n := 0
	for i := range 10000 {
		if w.Next(int64(i), now).Text == "A" {
			n++
		}
	}
	if n < 8700 || n > 9300 {
		t.Fatalf("weight 9:1 gave %d/10000 A", n)
	}
}

func TestSourcesAndDestinations(t *testing.T) {
	b := build(t, Config{Sources: Sources{Kind: "range", From: "0919800000", To: "0919800009"}, Destinations: []string{"a", "b", "c"}})
	var dests []string
	for i := range 6 {
		m := b.Next(int64(i), time.Now())
		if len(m.Src) != 10 || m.Src < "0919800000" || m.Src > "0919800009" {
			t.Fatalf("source %q outside range", m.Src)
		}
		dests = append(dests, m.Dst)
	}
	if strings.Join(dests, "") != "abcabc" {
		t.Fatalf("round-robin %v", dests)
	}
	p := build(t, Config{Sources: Sources{Kind: "prefix", Prefix: "9198", Digits: 8}})
	if s := p.Next(1, time.Now()).Src; len(s) != 12 || !strings.HasPrefix(s, "9198") {
		t.Fatalf("prefix source %q", s)
	}
	if _, err := NewBuilder(Config{Sources: Sources{Kind: "range", From: "9", To: "1"}, Destinations: []string{"x"}, Templates: []Template{{"x", 1}}}); err == nil {
		t.Fatal("reversed range accepted")
	}
}

func TestLongShare(t *testing.T) {
	b := build(t, Config{LongPct: 100, Templates: []Template{{Text: "Reply {seq}", Weight: 1}}})
	for i := range 50 {
		if n := len(b.Next(int64(i), time.Now()).Parts); n < 2 || n > 3 {
			t.Fatalf("long message has %d parts", n)
		}
	}
	u := build(t, Config{LongPct: 100, UCS2: true})
	if n := len(u.Next(1, time.Now()).Parts); n < 2 || n > 3 {
		t.Fatalf("long UCS2 message has %d parts", n)
	}
}
