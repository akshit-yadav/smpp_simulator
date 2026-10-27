package mo

// GSM 03.38 default alphabet: basic table by septet value, and the extension
// table reached through the escape septet 0x1B.
const gsmBasic = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞ\x1bÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?" +
	"¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"

const gsmEscape = 0x1B

var (
	gsmToSeptet = map[rune]byte{}
	gsmExt      = map[rune]byte{'\f': 0x0A, '^': 0x14, '{': 0x28, '}': 0x29, '\\': 0x2F, '[': 0x3C, '~': 0x3D, ']': 0x3E, '|': 0x40, '€': 0x65}
)

func init() {
	i := 0
	for _, r := range gsmBasic {
		if i != gsmEscape {
			gsmToSeptet[r] = byte(i)
		}
		i++
	}
	if i != 128 {
		panic("gsm basic table must have 128 entries")
	}
}

// EncodeGSM7 encodes text in the GSM default alphabet, one septet per octet
// (unpacked, as SMPP carries it). Characters outside the alphabet become '?'.
func EncodeGSM7(text string) []byte {
	out := make([]byte, 0, len(text))
	for _, r := range text {
		if c, ok := gsmToSeptet[r]; ok {
			out = append(out, c)
		} else if c, ok := gsmExt[r]; ok {
			out = append(out, gsmEscape, c)
		} else {
			out = append(out, '?')
		}
	}
	return out
}

// DecodeGSM7 is the inverse of EncodeGSM7 (for tests and diagnostics).
func DecodeGSM7(data []byte) string {
	basic := []rune(gsmBasic)
	ext := map[byte]rune{}
	for r, c := range gsmExt {
		ext[c] = r
	}
	out := make([]rune, 0, len(data))
	for i := 0; i < len(data); i++ {
		if data[i] == gsmEscape && i+1 < len(data) {
			i++
			out = append(out, ext[data[i]])
			continue
		}
		out = append(out, basic[data[i]&0x7F])
	}
	return string(out)
}
