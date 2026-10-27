// Package pdu encodes and decodes SMPP 3.4 PDUs.
//
// A Packet is a raw frame: header fields plus the undecoded body. Typed bodies
// (Bind, SM, ...) are decoded from and appended to byte slices, so the hot path
// can reuse buffers and only decode what it needs.
package pdu

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	HeaderLen = 16
	// MaxLen bounds command_length. short_message is at most 254 octets, but
	// message_payload may carry up to 64K.
	MaxLen = 70 * 1024
)

type CommandID uint32

const (
	GenericNack         CommandID = 0x80000000
	BindReceiver        CommandID = 0x00000001
	BindReceiverResp    CommandID = 0x80000001
	BindTransmitter     CommandID = 0x00000002
	BindTransmitterResp CommandID = 0x80000002
	QuerySM             CommandID = 0x00000003
	QuerySMResp         CommandID = 0x80000003
	SubmitSM            CommandID = 0x00000004
	SubmitSMResp        CommandID = 0x80000004
	DeliverSM           CommandID = 0x00000005
	DeliverSMResp       CommandID = 0x80000005
	Unbind              CommandID = 0x00000006
	UnbindResp          CommandID = 0x80000006
	ReplaceSM           CommandID = 0x00000007
	ReplaceSMResp       CommandID = 0x80000007
	CancelSM            CommandID = 0x00000008
	CancelSMResp        CommandID = 0x80000008
	BindTransceiver     CommandID = 0x00000009
	BindTransceiverResp CommandID = 0x80000009
	Outbind             CommandID = 0x0000000B
	EnquireLink         CommandID = 0x00000015
	EnquireLinkResp     CommandID = 0x80000015
	SubmitMulti         CommandID = 0x00000021
	SubmitMultiResp     CommandID = 0x80000021
	AlertNotification   CommandID = 0x00000102
	DataSM              CommandID = 0x00000103
	DataSMResp          CommandID = 0x80000103
)

var commandNames = map[CommandID]string{
	GenericNack: "generic_nack", BindReceiver: "bind_receiver", BindReceiverResp: "bind_receiver_resp",
	BindTransmitter: "bind_transmitter", BindTransmitterResp: "bind_transmitter_resp",
	QuerySM: "query_sm", QuerySMResp: "query_sm_resp", SubmitSM: "submit_sm", SubmitSMResp: "submit_sm_resp",
	DeliverSM: "deliver_sm", DeliverSMResp: "deliver_sm_resp", Unbind: "unbind", UnbindResp: "unbind_resp",
	ReplaceSM: "replace_sm", ReplaceSMResp: "replace_sm_resp", CancelSM: "cancel_sm", CancelSMResp: "cancel_sm_resp",
	BindTransceiver: "bind_transceiver", BindTransceiverResp: "bind_transceiver_resp", Outbind: "outbind",
	EnquireLink: "enquire_link", EnquireLinkResp: "enquire_link_resp", SubmitMulti: "submit_multi",
	SubmitMultiResp: "submit_multi_resp", AlertNotification: "alert_notification", DataSM: "data_sm", DataSMResp: "data_sm_resp",
}

func (c CommandID) String() string {
	if n, ok := commandNames[c]; ok {
		return n
	}
	return fmt.Sprintf("command(0x%08X)", uint32(c))
}

// IsResponse reports whether c is a response (high bit set).
func (c CommandID) IsResponse() bool { return c&0x80000000 != 0 }

// Resp returns the response command for a request.
func (c CommandID) Resp() CommandID { return c | 0x80000000 }

// Packet is one SMPP frame. Body aliases the reader's buffer when produced by
// Reader.Read and is only valid until the next Read.
type Packet struct {
	CommandID CommandID
	Status    Status
	Sequence  uint32
	Body      []byte
}

// Append encodes the packet onto dst.
func (p Packet) Append(dst []byte) []byte {
	return AppendPacket(dst, p.CommandID, p.Status, p.Sequence, p.Body)
}

// AppendPacket appends a full PDU with the given body.
func AppendPacket(dst []byte, id CommandID, status Status, seq uint32, body []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(HeaderLen+len(body)))
	dst = binary.BigEndian.AppendUint32(dst, uint32(id))
	dst = binary.BigEndian.AppendUint32(dst, uint32(status))
	dst = binary.BigEndian.AppendUint32(dst, seq)
	return append(dst, body...)
}

// Encode returns a freshly allocated PDU.
func Encode(id CommandID, status Status, seq uint32, body []byte) []byte {
	return AppendPacket(make([]byte, 0, HeaderLen+len(body)), id, status, seq, body)
}

// ErrInvalidLength is returned for a command_length outside [16, MaxLen].
// The stream cannot be resynchronised after it, so the connection must close.
var ErrInvalidLength = errors.New("smpp: invalid command_length")

// Reader reads Packets from a stream, reusing one body buffer.
type Reader struct {
	r   *bufio.Reader
	hdr [HeaderLen]byte
	buf []byte
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64*1024), buf: make([]byte, 0, 512)}
}

// Read returns the next packet. Its Body is valid until the next call.
func (r *Reader) Read() (Packet, error) {
	if _, err := io.ReadFull(r.r, r.hdr[:]); err != nil {
		return Packet{}, err
	}
	n := binary.BigEndian.Uint32(r.hdr[0:4])
	p := Packet{
		CommandID: CommandID(binary.BigEndian.Uint32(r.hdr[4:8])),
		Status:    Status(binary.BigEndian.Uint32(r.hdr[8:12])),
		Sequence:  binary.BigEndian.Uint32(r.hdr[12:16]),
	}
	if n < HeaderLen || n > MaxLen {
		return p, fmt.Errorf("%w: %d", ErrInvalidLength, n)
	}
	bodyLen := int(n) - HeaderLen
	if cap(r.buf) < bodyLen {
		r.buf = make([]byte, bodyLen)
	}
	r.buf = r.buf[:bodyLen]
	if _, err := io.ReadFull(r.r, r.buf); err != nil {
		return p, err
	}
	p.Body = r.buf
	return p, nil
}
