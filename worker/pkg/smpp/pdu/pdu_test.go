package pdu

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	var stream []byte
	stream = AppendPacket(stream, EnquireLink, ESME_ROK, 1, nil)
	stream = AppendPacket(stream, SubmitSMResp, ESME_ROK, 2, MessageIDResp{MessageID: "abc"}.Append(nil))
	stream = AppendPacket(stream, SubmitSMResp, ESME_RTHROTTLED, 3, nil)

	r := NewReader(bytes.NewReader(stream))
	want := []struct {
		id     CommandID
		status Status
		seq    uint32
		body   string
	}{
		{EnquireLink, ESME_ROK, 1, ""},
		{SubmitSMResp, ESME_ROK, 2, "abc\x00"},
		{SubmitSMResp, ESME_RTHROTTLED, 3, ""},
	}
	for _, w := range want {
		p, err := r.Read()
		if err != nil {
			t.Fatal(err)
		}
		if p.CommandID != w.id || p.Status != w.status || p.Sequence != w.seq || string(p.Body) != w.body {
			t.Fatalf("got %v %v %d %q, want %+v", p.CommandID, p.Status, p.Sequence, p.Body, w)
		}
	}
	if _, err := r.Read(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestReaderRejectsBadLength(t *testing.T) {
	for _, n := range []uint32{0, 15, MaxLen + 1} {
		hdr := []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n), 0, 0, 0, 0x15, 0, 0, 0, 0, 0, 0, 0, 1}
		_, err := NewReader(bytes.NewReader(hdr)).Read()
		if !errors.Is(err, ErrInvalidLength) {
			t.Errorf("length %d: got %v", n, err)
		}
	}
}

func TestReaderTruncatedBody(t *testing.T) {
	full := Encode(SubmitSMResp, 0, 1, []byte("abcdef\x00"))
	_, err := NewReader(bytes.NewReader(full[:len(full)-2])).Read()
	if err != io.ErrUnexpectedEOF {
		t.Fatalf("got %v", err)
	}
}

func TestBindRoundTrip(t *testing.T) {
	in := Bind{SystemID: "vendora", Password: "secret1", SystemType: "SMPP", InterfaceVersion: 0x34, AddrTON: 1, AddrNPI: 1, AddressRange: ""}
	out, err := DecodeBind(in.Append(nil))
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("got %+v", out)
	}
}

func TestBindFieldErrors(t *testing.T) {
	long := Bind{SystemID: "abcdefghijklmnop", Password: "x"}.Append(nil) // 16 chars + NUL > 16
	_, err := DecodeBind(long)
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "system_id" {
		t.Fatalf("got %v", err)
	}
	longPw := Bind{SystemID: "a", Password: "123456789"}.Append(nil)
	if _, err := DecodeBind(longPw); !errors.As(err, &fe) || fe.Field != "password" {
		t.Fatalf("got %v", err)
	}
	if _, err := DecodeBind([]byte("abc")); !errors.As(err, &fe) || fe.Field != "system_id" {
		t.Fatalf("missing NUL: got %v", err)
	}
	if _, err := DecodeBind([]byte("a\x00b\x00\x00")); !errors.As(err, &fe) || fe.Field != "interface_version" {
		t.Fatalf("truncated: got %v", err)
	}
}

func TestSMRoundTripWithTLVs(t *testing.T) {
	in := SM{
		ServiceType: "CMT", SourceAddrTON: 5, SourceAddrNPI: 0, SourceAddr: "HEXWIRE",
		DestAddrTON: 1, DestAddrNPI: 1, DestAddr: "919812345678",
		ESMClass: 0x40, RegisteredDelivery: 1, DataCoding: 8,
		ShortMessage: []byte("hello"),
		TLVs: TLVs{
			{Tag: TagSarMsgRefNum, Value: []byte{0, 7}},
			NetworkErrorCodeTLV(3, 0x0001),
		},
	}
	out, err := DecodeSM(in.Append(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("got %+v\nwant %+v", out, in)
	}
	if v, ok := out.TLVs.Get(TagNetworkErrorCode); !ok || !bytes.Equal(v, []byte{3, 0, 1}) {
		t.Fatalf("network_error_code = %v", v)
	}
}

func TestSMMessagePayload(t *testing.T) {
	sm := SM{TLVs: TLVs{{Tag: TagMessagePayload, Value: []byte("long text")}}}
	out, err := DecodeSM(sm.Append(nil))
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Message()) != "long text" {
		t.Fatalf("got %q", out.Message())
	}
}

func TestSMTruncatedShortMessage(t *testing.T) {
	b := SM{ShortMessage: []byte("hello")}.Append(nil)
	_, err := DecodeSM(b[:len(b)-2])
	var fe *FieldError
	if !errors.As(err, &fe) || fe.Field != "short_message" {
		t.Fatalf("got %v", err)
	}
}

func TestSMTruncatedTLV(t *testing.T) {
	b := SM{TLVs: TLVs{{Tag: TagSarMsgRefNum, Value: []byte{0, 7}}}}.Append(nil)
	if _, err := DecodeSM(b[:len(b)-1]); err == nil {
		t.Fatal("want error")
	}
}

func TestStatusNames(t *testing.T) {
	if ESME_RTHROTTLED.String() != "ESME_RTHROTTLED" {
		t.Fatal(ESME_RTHROTTLED.String())
	}
	if !Status(0x400).IsVendor() || !Status(0x4FF).IsVendor() || Status(0x500).IsVendor() || Status(0x3FF).IsVendor() {
		t.Fatal("vendor range")
	}
	if Status(0x401).String() != "VENDOR(0x401)" {
		t.Fatal(Status(0x401).String())
	}
}

func BenchmarkDecodeSM(b *testing.B) {
	body := SM{SourceAddr: "HEXWIRE", DestAddr: "919812345678", RegisteredDelivery: 1, ShortMessage: []byte("Your OTP is 123456")}.Append(nil)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodeSM(body); err != nil {
			b.Fatal(err)
		}
	}
}
