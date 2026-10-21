package pdu

import "encoding/binary"

// Tag is an optional-parameter (TLV) tag.
type Tag uint16

const (
	TagUserMessageReference Tag = 0x0204
	TagSarMsgRefNum         Tag = 0x020C
	TagSarTotalSegments     Tag = 0x020E
	TagSarSegmentSeqnum     Tag = 0x020F
	TagSCInterfaceVersion   Tag = 0x0210
	TagReceiptedMessageID   Tag = 0x001E
	TagNetworkErrorCode     Tag = 0x0423
	TagMessagePayload       Tag = 0x0424
	TagMessageState         Tag = 0x0427
)

type TLV struct {
	Tag   Tag
	Value []byte
}

type TLVs []TLV

func (ts TLVs) Get(tag Tag) ([]byte, bool) {
	for _, t := range ts {
		if t.Tag == tag {
			return t.Value, true
		}
	}
	return nil, false
}

func (ts TLVs) Append(dst []byte) []byte {
	for _, t := range ts {
		dst = binary.BigEndian.AppendUint16(dst, uint16(t.Tag))
		dst = binary.BigEndian.AppendUint16(dst, uint16(len(t.Value)))
		dst = append(dst, t.Value...)
	}
	return dst
}

// CStringTLV builds a TLV whose value is a NUL-terminated string.
func CStringTLV(tag Tag, s string) TLV {
	return TLV{Tag: tag, Value: append([]byte(s), 0)}
}

// ByteTLV builds a single-octet TLV.
func ByteTLV(tag Tag, b byte) TLV { return TLV{Tag: tag, Value: []byte{b}} }

// NetworkErrorCodeTLV builds network_error_code: network type (1=ANSI-136,
// 2=IS-95, 3=GSM, ...) followed by a 2-octet error code.
func NetworkErrorCodeTLV(networkType byte, code uint16) TLV {
	return TLV{Tag: TagNetworkErrorCode, Value: []byte{networkType, byte(code >> 8), byte(code)}}
}
