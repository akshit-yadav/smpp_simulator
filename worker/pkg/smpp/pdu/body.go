package pdu

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Maximum C-Octet String sizes, including the NUL terminator (SMPP 3.4 §4).
const (
	maxSystemID     = 16
	maxPassword     = 9
	maxSystemType   = 13
	maxAddressRange = 41
	maxServiceType  = 6
	maxAddr         = 21
	maxTime         = 17
	maxMessageID    = 65
)

// FieldError reports which field of a body failed to decode, so callers can
// answer with the matching command_status.
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return fmt.Sprintf("smpp: field %s: %v", e.Field, e.Err) }
func (e *FieldError) Unwrap() error { return e.Err }

var (
	errTruncated = errors.New("truncated")
	errTooLong   = errors.New("too long")
)

// Bind is the body of bind_transmitter, bind_receiver and bind_transceiver.
type Bind struct {
	SystemID         string
	Password         string
	SystemType       string
	InterfaceVersion byte
	AddrTON          byte
	AddrNPI          byte
	AddressRange     string
}

func DecodeBind(b []byte) (Bind, error) {
	d := decoder{b: b}
	v := Bind{
		SystemID:   d.cstring("system_id", maxSystemID),
		Password:   d.cstring("password", maxPassword),
		SystemType: d.cstring("system_type", maxSystemType),
	}
	v.InterfaceVersion = d.byte("interface_version")
	v.AddrTON = d.byte("addr_ton")
	v.AddrNPI = d.byte("addr_npi")
	v.AddressRange = d.cstring("address_range", maxAddressRange)
	return v, d.err
}

func (v Bind) Append(dst []byte) []byte {
	dst = appendCString(dst, v.SystemID)
	dst = appendCString(dst, v.Password)
	dst = appendCString(dst, v.SystemType)
	dst = append(dst, v.InterfaceVersion, v.AddrTON, v.AddrNPI)
	return appendCString(dst, v.AddressRange)
}

// SM is the body of submit_sm and deliver_sm, which share one layout.
type SM struct {
	ServiceType          string
	SourceAddrTON        byte
	SourceAddrNPI        byte
	SourceAddr           string
	DestAddrTON          byte
	DestAddrNPI          byte
	DestAddr             string
	ESMClass             byte
	ProtocolID           byte
	PriorityFlag         byte
	ScheduleDeliveryTime string
	ValidityPeriod       string
	RegisteredDelivery   byte
	ReplaceIfPresent     byte
	DataCoding           byte
	SMDefaultMsgID       byte
	ShortMessage         []byte
	TLVs                 TLVs
}

func DecodeSM(b []byte) (SM, error) {
	d := decoder{b: b}
	var v SM
	v.ServiceType = d.cstring("service_type", maxServiceType)
	v.SourceAddrTON = d.byte("source_addr_ton")
	v.SourceAddrNPI = d.byte("source_addr_npi")
	v.SourceAddr = d.cstring("source_addr", maxAddr)
	v.DestAddrTON = d.byte("dest_addr_ton")
	v.DestAddrNPI = d.byte("dest_addr_npi")
	v.DestAddr = d.cstring("destination_addr", maxAddr)
	v.ESMClass = d.byte("esm_class")
	v.ProtocolID = d.byte("protocol_id")
	v.PriorityFlag = d.byte("priority_flag")
	v.ScheduleDeliveryTime = d.cstring("schedule_delivery_time", maxTime)
	v.ValidityPeriod = d.cstring("validity_period", maxTime)
	v.RegisteredDelivery = d.byte("registered_delivery")
	v.ReplaceIfPresent = d.byte("replace_if_present_flag")
	v.DataCoding = d.byte("data_coding")
	v.SMDefaultMsgID = d.byte("sm_default_msg_id")
	n := int(d.byte("sm_length"))
	v.ShortMessage = d.bytes("short_message", n)
	v.TLVs = d.tlvs()
	return v, d.err
}

func (v SM) Append(dst []byte) []byte {
	dst = appendCString(dst, v.ServiceType)
	dst = append(dst, v.SourceAddrTON, v.SourceAddrNPI)
	dst = appendCString(dst, v.SourceAddr)
	dst = append(dst, v.DestAddrTON, v.DestAddrNPI)
	dst = appendCString(dst, v.DestAddr)
	dst = append(dst, v.ESMClass, v.ProtocolID, v.PriorityFlag)
	dst = appendCString(dst, v.ScheduleDeliveryTime)
	dst = appendCString(dst, v.ValidityPeriod)
	dst = append(dst, v.RegisteredDelivery, v.ReplaceIfPresent, v.DataCoding, v.SMDefaultMsgID)
	dst = append(dst, byte(len(v.ShortMessage)))
	dst = append(dst, v.ShortMessage...)
	return v.TLVs.Append(dst)
}

// Message returns the message text: short_message, or the message_payload
// TLV when short_message is empty.
func (v SM) Message() []byte {
	if len(v.ShortMessage) == 0 {
		if p, ok := v.TLVs.Get(TagMessagePayload); ok {
			return p
		}
	}
	return v.ShortMessage
}

// MessageIDResp is the body of submit_sm_resp, deliver_sm_resp and data_sm_resp.
type MessageIDResp struct {
	MessageID string
}

func DecodeMessageIDResp(b []byte) (MessageIDResp, error) {
	if len(b) == 0 { // error responses may omit the body
		return MessageIDResp{}, nil
	}
	d := decoder{b: b}
	v := MessageIDResp{MessageID: d.cstring("message_id", maxMessageID)}
	return v, d.err
}

func (v MessageIDResp) Append(dst []byte) []byte { return appendCString(dst, v.MessageID) }

// BindResp is the body of the bind_*_resp PDUs.
type BindResp struct {
	SystemID string
	TLVs     TLVs
}

func DecodeBindResp(b []byte) (BindResp, error) {
	if len(b) == 0 {
		return BindResp{}, nil
	}
	d := decoder{b: b}
	v := BindResp{SystemID: d.cstring("system_id", maxSystemID)}
	v.TLVs = d.tlvs()
	return v, d.err
}

func (v BindResp) Append(dst []byte) []byte {
	return v.TLVs.Append(appendCString(dst, v.SystemID))
}

func appendCString(dst []byte, s string) []byte {
	dst = append(dst, s...)
	return append(dst, 0)
}

// decoder reads fields sequentially and remembers the first error.
type decoder struct {
	b   []byte
	off int
	err error
}

func (d *decoder) fail(field string, err error) {
	if d.err == nil {
		d.err = &FieldError{Field: field, Err: err}
	}
}

func (d *decoder) cstring(field string, max int) string {
	if d.err != nil {
		return ""
	}
	for i := d.off; i < len(d.b); i++ {
		if d.b[i] == 0 {
			if i-d.off+1 > max {
				d.fail(field, errTooLong)
				return ""
			}
			s := string(d.b[d.off:i])
			d.off = i + 1
			return s
		}
	}
	d.fail(field, errTruncated)
	return ""
}

func (d *decoder) byte(field string) byte {
	if d.err != nil {
		return 0
	}
	if d.off >= len(d.b) {
		d.fail(field, errTruncated)
		return 0
	}
	c := d.b[d.off]
	d.off++
	return c
}

func (d *decoder) bytes(field string, n int) []byte {
	if d.err != nil {
		return nil
	}
	if d.off+n > len(d.b) {
		d.fail(field, errTruncated)
		return nil
	}
	out := make([]byte, n)
	copy(out, d.b[d.off:])
	d.off += n
	return out
}

func (d *decoder) tlvs() TLVs {
	var out TLVs
	for d.err == nil && d.off < len(d.b) {
		if d.off+4 > len(d.b) {
			d.fail("tlv", errTruncated)
			return out
		}
		tag := binary.BigEndian.Uint16(d.b[d.off:])
		n := int(binary.BigEndian.Uint16(d.b[d.off+2:]))
		d.off += 4
		val := d.bytes(fmt.Sprintf("tlv 0x%04X", tag), n)
		out = append(out, TLV{Tag: Tag(tag), Value: val})
	}
	return out
}
