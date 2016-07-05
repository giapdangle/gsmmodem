// Package sms allows to encode and decode SMS messages into/from PDU format as described in 3GPP TS 23.040.
package sms

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/xlab/at/pdu"
)

// Common errors.
var (
	ErrUnknownEncoding    = errors.New("sms: unsupported encoding")
	ErrUnknownMessageType = errors.New("sms: unsupported message type")
	ErrIncorrectSize      = errors.New("sms: decoded incorrect size of field")
	ErrNonRelative        = errors.New("sms: non-relative validity period support is not implemented yet")
)

// MessageType represents the message's type.
type MessageType byte

// MessageTypes represent the possible message's types (3GPP TS 23.040).
var MessageTypes = struct {
	Deliver       MessageType
	DeliverReport MessageType
	StatusReport  MessageType
	Command       MessageType
	Submit        MessageType
	SubmitReport  MessageType
}{
	0x00, 0x00,
	0x02, 0x02,
	0x01, 0x01,
}

// ValidityPeriodFormat represents the format of message's validity period.
type ValidityPeriodFormat byte

// ValidityPeriodFormats represent the possible formats of message's
// validity period (3GPP TS 23.040).
var ValidityPeriodFormats = struct {
	FieldNotPresent ValidityPeriodFormat
	Relative        ValidityPeriodFormat
	Enhanced        ValidityPeriodFormat
	Absolute        ValidityPeriodFormat
}{
	0x00, 0x02, 0x01, 0x03,
}

// Encoding represents the encoding of message's text data.
type Encoding byte

// Encodings represent the possible encodings of message's text data.
var Encodings = struct {
	Gsm7Bit Encoding
	UCS2    Encoding
}{
	0x00, 0x08,
}

// PhoneNumber represents the address in either local or international format.
type PhoneNumber string

// PDU returns the number of digits in address and octets of semi-octet encoded address.
func (p PhoneNumber) PDU() (int, []byte, error) {
	digitStr := strings.TrimPrefix(string(p), "+")
	var str string
	for _, r := range digitStr {
		if r >= '0' && r <= '9' {
			str = str + string(r)
		}
	}
	n := len(str)
	number, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return 0, nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte(p.Type())
	buf.Write(pdu.EncodeSemi(number))
	return n, buf.Bytes(), nil
}

// Type returns the type of address — local or international.
func (p PhoneNumber) Type() byte {
	if strings.HasPrefix(string(p), "+") {
		return (0x81 | 0x10) // 1001 0001
	}
	return (0x81 | 0x20) // 1010 0001
}

// ReadFrom constructs an address from the semi-decoded version in the supplied byte slice.
func (p *PhoneNumber) ReadFrom(octets []byte) {
	if len(octets) < 1 {
		return
	}
	addrType := octets[0]
	addr := pdu.DecodeSemiAddress(octets[1:])
	if addrType&0x10 > 0 {
		*p = PhoneNumber("+" + addr)
	} else {
		*p = PhoneNumber(addr)
	}
	return
}

// USSD represents an USSD query string
type USSD string

// Gsm7Bit encodes USSD query into GSM 7-Bit packed octets.
func (u USSD) Gsm7Bit() []byte {
	return pdu.Encode7Bit(string(u))
}

// ValidityPeriod represents the validity period of message.
type ValidityPeriod time.Duration

// Octet return a one-byte representation of the validity period.
func (v ValidityPeriod) Octet() byte {
	switch d := time.Duration(v); {
	case d/time.Minute < 5:
		return 0x00
	case d/time.Hour < 12:
		return byte(d / (time.Minute * 5))
	case d/time.Hour < 24:
		return byte((d-d/time.Hour*12)/(time.Minute*30) + 143)
	case d/time.Hour < 744:
		days := d / (time.Hour * 24)
		return byte(days + 166)
	default:
		weeks := d / (time.Hour * 24 * 7)
		if weeks > 62 {
			return 0xFF
		}
		return byte(weeks + 192)
	}
}

// ReadFrom reads the validity period form the given byte.
func (v *ValidityPeriod) ReadFrom(oct byte) {
	switch n := time.Duration(oct); {
	case n >= 0 && n <= 143:
		*v = ValidityPeriod(5 * time.Minute * n)
	case n >= 144 && n <= 167:
		*v = ValidityPeriod(12*time.Hour + 30*time.Minute*(n-143))
	case n >= 168 && n <= 196:
		*v = ValidityPeriod(24 * time.Hour * (n - 166))
	case n >= 197 && n <= 255:
		*v = ValidityPeriod(7 * 24 * time.Hour * (n - 192))
	}
}

// Timestamp represents message's timestamp.
type Timestamp time.Time

// PDU returns bytes of semi-octet encoded timestamp.
func (t Timestamp) PDU() []byte {
	date := time.Time(t)
	year := uint64(date.Year() - 2000)
	month := uint64(date.Month())
	day := uint64(date.Day())
	hour := uint64(date.Hour())
	minute := uint64(date.Minute())
	second := uint64(date.Second())
	_, offset := date.Zone()
	quarters := uint64(offset / int(time.Hour/time.Second) * 4)
	return pdu.EncodeSemi(year, month, day, hour, minute, second, quarters)
}

// ReadFrom reads a semi-encoded timestamp from the given octets.
func (t *Timestamp) ReadFrom(octets []byte) {
	blocks := pdu.DecodeSemi(octets)
	date := time.Date(2000+blocks[0], time.Month(blocks[1]), blocks[2], blocks[3], blocks[4], blocks[5], 0, time.UTC)
	diff := time.Duration(blocks[6]) * 15 * time.Minute
	if blocks[6]>>3&0x01 == 1 { // bit 3 = GMT offset sgn
		// was negative, so make UTC
		date = date.Add(diff)
	} else {
		// was positive, so make UTC
		date = date.Add(-diff)
	}
	*t = Timestamp(date.In(time.Local))
}

// Message represents an SMS message, including some advanced fields. This
// is a user-friendly high-level representation that should be used around.
// Complies with 3GPP TS 23.040.
type Message struct {
	Type                 MessageType
	Encoding             Encoding
	VP                   ValidityPeriod
	VPFormat             ValidityPeriodFormat
	ServiceCenterTime    Timestamp
	ServiceCenterAddress PhoneNumber
	Address              PhoneNumber
	Text                 string

	// Advanced
	MessageReference         byte
	ReplyPathExists          bool
	UserDataStartsWithHeader bool
	StatusReportIndication   bool
	StatusReportRequest      bool
	MoreMessagesToSend       bool
	LoopPrevention           bool
	RejectDuplicates         bool
}

func blocks(n, block int) int {
	if n%block == 0 {
		return n / block
	}
	return n/block + 1
}

// PDU serializes the message into octets ready to be transferred.
// Returns the number of TPDU bytes in the produced PDU.
// Complies with 3GPP TS 23.040.
func (s *Message) PDU() (int, []byte, error) {
	var buf bytes.Buffer
	if len(s.ServiceCenterAddress) < 1 {
		buf.WriteByte(0x00) // SMSC info length
	} else {
		_, octets, err := s.ServiceCenterAddress.PDU()
		if err != nil {
			return 0, nil, err
		}
		buf.WriteByte(byte(len(octets)))
		buf.Write(octets)
	}

	switch s.Type {
	case MessageTypes.Deliver:
		var sms smsDeliver
		sms.MessageTypeIndicator = byte(s.Type)
		sms.MoreMessagesToSend = s.MoreMessagesToSend
		sms.LoopPrevention = s.LoopPrevention
		sms.ReplyPath = s.ReplyPathExists
		sms.UserDataHeaderIndicator = s.UserDataStartsWithHeader
		sms.StatusReportIndication = s.StatusReportIndication

		addrLen, addr, err := s.Address.PDU()
		if err != nil {
			return 0, nil, err
		}
		var addrBuf bytes.Buffer
		addrBuf.WriteByte(byte(addrLen))
		addrBuf.Write(addr)
		sms.OriginatingAddress = addrBuf.Bytes()

		sms.ProtocolIdentifier = 0x00 // Short Message Type 0
		sms.DataCodingScheme = byte(s.Encoding)
		sms.ServiceCentreTimestamp = s.ServiceCenterTime.PDU()

		var userData []byte
		if s.Encoding == Encodings.Gsm7Bit {
			userData = pdu.Encode7Bit(s.Text)
			sms.UserDataLength = byte(len(s.Text))
		} else if s.Encoding == Encodings.UCS2 {
			userData = pdu.EncodeUcs2(s.Text)
			sms.UserDataLength = byte(len(userData))
		} else {
			return 0, nil, ErrUnknownEncoding
		}

		sms.UserDataLength = byte(len(userData))
		sms.UserData = userData
		n, err := buf.Write(sms.Bytes())
		if err != nil {
			return 0, nil, err
		}
		return n, buf.Bytes(), nil
	case MessageTypes.Submit:
		var sms smsSubmit
		sms.MessageTypeIndicator = byte(s.Type)
		sms.RejectDuplicates = s.RejectDuplicates
		sms.ValidityPeriodFormat = byte(s.VPFormat)
		sms.ReplyPath = s.ReplyPathExists
		sms.UserDataHeaderIndicator = s.UserDataStartsWithHeader
		sms.StatusReportRequest = s.StatusReportRequest
		sms.MessageReference = s.MessageReference

		addrLen, addr, err := s.Address.PDU()
		if err != nil {
			return 0, nil, err
		}
		var addrBuf bytes.Buffer
		addrBuf.WriteByte(byte(addrLen))
		addrBuf.Write(addr)
		sms.DestinationAddress = addrBuf.Bytes()

		sms.ProtocolIdentifier = 0x00 // Short Message Type 0
		sms.DataCodingScheme = byte(s.Encoding)

		switch s.VPFormat {
		case ValidityPeriodFormats.Relative:
			sms.ValidityPeriod = byte(s.VP.Octet())
		case ValidityPeriodFormats.Absolute, ValidityPeriodFormats.Enhanced:
			return 0, nil, ErrNonRelative
		}

		var userData []byte
		if s.Encoding == Encodings.Gsm7Bit {
			userData = pdu.Encode7Bit(s.Text)
			sms.UserDataLength = byte(len(s.Text))
		} else if s.Encoding == Encodings.UCS2 {
			userData = pdu.EncodeUcs2(s.Text)
			sms.UserDataLength = byte(len(userData))
		} else {
			return 0, nil, ErrUnknownEncoding
		}

		sms.UserData = userData
		n, err := buf.Write(sms.Bytes())
		if err != nil {
			return 0, nil, err
		}
		return n, buf.Bytes(), nil
	default:
		return 0, nil, ErrUnknownMessageType
	}
}

// ReadFrom constructs a message from the supplied PDU octets. Returns the number of bytes read.
// Complies with 3GPP TS 23.040.
func (s *Message) ReadFrom(octets []byte) (n int, err error) {
	*s = Message{}
	buf := bytes.NewReader(octets)
	scLen, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	if scLen > 12 {
		return 0, ErrIncorrectSize
	}
	addr := make([]byte, scLen)
	off, err := io.ReadFull(buf, addr)
	n += off
	if err != nil {
		return
	}
	s.ServiceCenterAddress.ReadFrom(addr)
	msgType, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	n--
	buf.UnreadByte()
	s.Type = MessageType(msgType & 0x03)

	switch s.Type {
	case MessageTypes.Deliver:
		var sms smsDeliver
		off, err2 := sms.FromBytes(octets[1+scLen:])
		n += off
		if err2 != nil {
			return n, err2
		}
		s.MoreMessagesToSend = sms.MoreMessagesToSend
		s.LoopPrevention = sms.LoopPrevention
		s.ReplyPathExists = sms.ReplyPath
		s.UserDataStartsWithHeader = sms.UserDataHeaderIndicator
		s.StatusReportIndication = sms.StatusReportIndication
		s.Address.ReadFrom(sms.OriginatingAddress[1:])
		s.Encoding = Encoding(sms.DataCodingScheme)
		s.ServiceCenterTime.ReadFrom(sms.ServiceCentreTimestamp)
		switch s.Encoding {
		case Encodings.Gsm7Bit:
			s.Text, err = pdu.Decode7Bit(sms.UserData)
			if err != nil {
				return
			}
			s.Text = cutStr(s.Text, int(sms.UserDataLength))
		case Encodings.UCS2:
			s.Text, err = pdu.DecodeUcs2(sms.UserData)
			if err != nil {
				return
			}
		default:
			return 0, ErrUnknownEncoding
		}
	case MessageTypes.Submit:
		var sms smsSubmit
		off, err2 := sms.FromBytes(octets[1+scLen:])
		n += off
		if err2 != nil {
			return n, err2
		}
		s.RejectDuplicates = sms.RejectDuplicates

		switch s.VPFormat {
		case ValidityPeriodFormats.Absolute, ValidityPeriodFormats.Enhanced:
			return n, ErrNonRelative
		default:
			s.VPFormat = ValidityPeriodFormat(sms.ValidityPeriodFormat)
		}

		s.ReplyPathExists = sms.ReplyPath
		s.UserDataStartsWithHeader = sms.UserDataHeaderIndicator
		s.StatusReportRequest = sms.StatusReportRequest
		s.Address.ReadFrom(sms.DestinationAddress[1:])
		s.Encoding = Encoding(sms.DataCodingScheme)

		if s.VPFormat != ValidityPeriodFormats.FieldNotPresent {
			s.VP.ReadFrom(sms.ValidityPeriod)
		}

		switch s.Encoding {
		case Encodings.Gsm7Bit:
			s.Text, err = pdu.Decode7Bit(sms.UserData)
			if err != nil {
				return
			}
			s.Text = cutStr(s.Text, int(sms.UserDataLength))
		case Encodings.UCS2:
			s.Text, err = pdu.DecodeUcs2(sms.UserData)
			if err != nil {
				return
			}
		default:
			return 0, ErrUnknownEncoding
		}
	default:
		return n, ErrUnknownMessageType
	}

	return
}

// Low-level representation of an deliver-type SMS message (3GPP TS 23.040).
type smsDeliver struct {
	MessageTypeIndicator    byte
	MoreMessagesToSend      bool
	LoopPrevention          bool
	ReplyPath               bool
	UserDataHeaderIndicator bool
	StatusReportIndication  bool
	// =========================
	OriginatingAddress     []byte
	ProtocolIdentifier     byte
	DataCodingScheme       byte
	ServiceCentreTimestamp []byte
	UserDataLength         byte
	UserData               []byte
}

func (s *smsDeliver) Bytes() []byte {
	var buf bytes.Buffer
	header := s.MessageTypeIndicator // 0-1 bits
	if !s.MoreMessagesToSend {
		header |= 0x01 << 2 // 2 bit
	}
	if s.LoopPrevention {
		header |= 0x01 << 3 // 3 bit
	}
	if s.StatusReportIndication {
		header |= 0x01 << 4 // 4 bit
	}
	if s.UserDataHeaderIndicator {
		header |= 0x01 << 5 // 5 bit
	}
	if s.ReplyPath {
		header |= 0x01 << 6 // 6 bit
	}
	buf.WriteByte(header)
	buf.Write(s.OriginatingAddress)
	buf.WriteByte(s.ProtocolIdentifier)
	buf.WriteByte(s.DataCodingScheme)
	buf.Write(s.ServiceCentreTimestamp)
	buf.WriteByte(s.UserDataLength)
	buf.Write(s.UserData)
	return buf.Bytes()
}

func (s *smsDeliver) FromBytes(octets []byte) (n int, err error) {
	buf := bytes.NewReader(octets)
	*s = smsDeliver{}
	header, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.MessageTypeIndicator = header & 0x03
	if header>>2&0x01 == 0x00 {
		s.MoreMessagesToSend = true
	}
	if header>>3&0x01 == 0x01 {
		s.LoopPrevention = true
	}
	if header>>4&0x01 == 0x01 {
		s.StatusReportIndication = true
	}
	if header>>5&0x01 == 0x01 {
		s.UserDataHeaderIndicator = true
	}
	if header>>6&0x01 == 0x01 {
		s.ReplyPath = true
	}
	oaLen, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	if oaLen > 12 {
		return n, ErrIncorrectSize
	}
	buf.UnreadByte() // will read length again
	n--
	s.OriginatingAddress = make([]byte, blocks(int(oaLen), 2)+2)
	off, err := io.ReadFull(buf, s.OriginatingAddress)
	n += off
	if err != nil {
		return
	}
	s.ProtocolIdentifier, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.DataCodingScheme, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.ServiceCentreTimestamp = make([]byte, 7)
	off, err = io.ReadFull(buf, s.ServiceCentreTimestamp)
	n += off
	if err != nil {
		return
	}
	s.UserDataLength, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.UserData = make([]byte, int(s.UserDataLength))
	off, _ = io.ReadFull(buf, s.UserData)
	s.UserData = s.UserData[:off]
	n += off
	return
}

// Low-level representation of an submit-type SMS message (3GPP TS 23.040).
type smsSubmit struct {
	MessageTypeIndicator    byte
	RejectDuplicates        bool
	ValidityPeriodFormat    byte
	ReplyPath               bool
	UserDataHeaderIndicator bool
	StatusReportRequest     bool
	// =========================
	MessageReference   byte
	DestinationAddress []byte
	ProtocolIdentifier byte
	DataCodingScheme   byte
	ValidityPeriod     byte
	UserDataLength     byte
	UserData           []byte
}

func (s *smsSubmit) Bytes() []byte {
	var buf bytes.Buffer
	header := s.MessageTypeIndicator // 0-1 bits
	if s.RejectDuplicates {
		header |= 0x01 << 2 // 2 bit
	}
	header |= s.ValidityPeriodFormat << 3 // 3-4 bits
	if s.StatusReportRequest {
		header |= 0x01 << 5 // 5 bit
	}
	if s.UserDataHeaderIndicator {
		header |= 0x01 << 6 // 6 bit
	}
	if s.ReplyPath {
		header |= 0x01 << 7 // 7 bit
	}
	buf.WriteByte(header)
	buf.WriteByte(s.MessageReference)
	buf.Write(s.DestinationAddress)
	buf.WriteByte(s.ProtocolIdentifier)
	buf.WriteByte(s.DataCodingScheme)
	if ValidityPeriodFormat(s.ValidityPeriodFormat) != ValidityPeriodFormats.FieldNotPresent {
		buf.WriteByte(s.ValidityPeriod)
	}
	buf.WriteByte(s.UserDataLength)
	buf.Write(s.UserData)
	return buf.Bytes()
}

func (s *smsSubmit) FromBytes(octets []byte) (n int, err error) {
	*s = smsSubmit{}
	buf := bytes.NewReader(octets)
	header, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.MessageTypeIndicator = header & 0x03
	if header&(0x01<<2) > 0 {
		s.RejectDuplicates = true
	}
	s.ValidityPeriodFormat = header >> 3 & 0x03
	if header&(0x01<<5) > 0 {
		s.StatusReportRequest = true
	}
	if header&(0x01<<6) > 0 {
		s.UserDataHeaderIndicator = true
	}
	if header&(0x01<<7) > 0 {
		s.ReplyPath = true
	}
	s.MessageReference, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	daLen, err := buf.ReadByte()
	n++
	if err != nil {
		return
	}
	if daLen > 12 {
		return n, ErrIncorrectSize
	}
	buf.UnreadByte() // read length again
	n--
	s.DestinationAddress = make([]byte, blocks(int(daLen), 2)+2)
	off, err := io.ReadFull(buf, s.DestinationAddress)
	n += off
	if err != nil {
		return
	}
	s.ProtocolIdentifier, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.DataCodingScheme, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	if ValidityPeriodFormat(s.ValidityPeriodFormat) != ValidityPeriodFormats.FieldNotPresent {
		s.ValidityPeriod, err = buf.ReadByte()
		n++
		if err != nil {
			return
		}
	}
	s.UserDataLength, err = buf.ReadByte()
	n++
	if err != nil {
		return
	}
	s.UserData = make([]byte, int(s.UserDataLength))
	off, _ = io.ReadFull(buf, s.UserData)
	s.UserData = s.UserData[:off]
	n += off
	return
}

func cutStr(str string, n int) string {
	runes := []rune(str)
	if n < len(str) {
		return string(runes[0:n])
	}
	return str
}
