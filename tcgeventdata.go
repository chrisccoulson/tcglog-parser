package tcglog

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"unsafe"
)

type invalidSpecIdEventError struct {
	s string
}

func (e invalidSpecIdEventError) Error() string {
	return fmt.Sprintf("invalid SpecIdEvent (%s)", e.s)
}

// EFISpecIdEventAlgorithmSize represents a digest algorithm and its length.
type EFISpecIdEventAlgorithmSize struct {
	AlgorithmId AlgorithmId
	DigestSize  uint16
}

type NoActionEventType int

const (
	UnknownNoActionEvent NoActionEventType = iota
	SpecId
	StartupLocality
	BiosIntegrityMeasurement
)

type NoActionEventData interface {
	Type() NoActionEventType
}

// SpecIdEventData corresponds to the event data for a Specification ID Version event
// (TCG_PCClientSpecIdEventStruct, TCG_EfiSpecIdEventStruct, TCG_EfiSpecIdEvent)
type SpecIdEventData struct {
	data             []byte
	Spec             Spec
	PlatformClass    uint32
	SpecVersionMinor uint8
	SpecVersionMajor uint8
	SpecErrata       uint8
	UintnSize        uint8
	DigestSizes      []EFISpecIdEventAlgorithmSize // The digest algorithms contained within this log
	VendorInfo       []byte
}

func (e *SpecIdEventData) String() string {
	var builder bytes.Buffer
	switch e.Spec {
	case SpecPCClient:
		builder.WriteString("PCClientSpecIdEvent")
	case SpecEFI_1_2, SpecEFI_2:
		builder.WriteString("EfiSpecIDEvent")
	}

	fmt.Fprintf(&builder, "{ spec=%d, platformClass=%d, specVersionMinor=%d, specVersionMajor=%d, "+
		"specErrata=%d", e.Spec, e.PlatformClass, e.SpecVersionMinor, e.SpecVersionMajor, e.SpecErrata)
	if e.Spec == SpecEFI_2 {
		builder.WriteString(", digestSizes=[")
		for i, algSize := range e.DigestSizes {
			if i > 0 {
				builder.WriteString(", ")
			}
			fmt.Fprintf(&builder, "{ algorithmId=0x%04x, digestSize=%d }",
				uint16(algSize.AlgorithmId), algSize.DigestSize)
		}
		builder.WriteString("]")
	}
	builder.WriteString(" }")
	return builder.String()
}

func (e *SpecIdEventData) Bytes() []byte {
	return e.data
}

func (e *SpecIdEventData) Type() NoActionEventType {
	return SpecId
}

func wrapSpecIdEventReadError(origErr error) error {
	if origErr == io.EOF {
		return invalidSpecIdEventError{"not enough data"}
	}

	return invalidSpecIdEventError{origErr.Error()}
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf
//  (section 11.3.4.1 "Specification Event")
func parsePCClientSpecIdEvent(stream io.Reader, eventData *SpecIdEventData) error {
	eventData.Spec = SpecPCClient

	// TCG_PCClientSpecIdEventStruct.vendorInfoSize
	var vendorInfoSize uint8
	if err := binary.Read(stream, binary.LittleEndian, &vendorInfoSize); err != nil {
		return wrapSpecIdEventReadError(err)
	}

	// TCG_PCClientSpecIdEventStruct.vendorInfo
	eventData.VendorInfo = make([]byte, vendorInfoSize)
	if _, err := io.ReadFull(stream, eventData.VendorInfo); err != nil {
		return wrapSpecIdEventReadError(err)
	}

	return nil
}

type specIdEventCommon struct {
	PlatformClass uint32
	SpecVersionMinor uint8
	SpecVersionMajor uint8
	SpecErrata uint8
	UintnSize uint8
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf
//  (section 11.3.4.1 "Specification Event")
// https://trustedcomputinggroup.org/wp-content/uploads/TCG_EFI_Platform_1_22_Final_-v15.pdf
//  (section 7.4 "EV_NO_ACTION Event Types")
// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientSpecPlat_TPM_2p0_1p04_pub.pdf
//  (secion 9.4.5.1 "Specification ID Version Event")
func decodeSpecIdEvent(stream io.Reader, data []byte, helper func(io.Reader, *SpecIdEventData) error) (*SpecIdEventData, error) {
	var common struct{
		PlatformClass uint32
		SpecVersionMinor uint8
		SpecVersionMajor uint8
		SpecErrata uint8
		UintnSize uint8
	}
	if err := binary.Read(stream, binary.LittleEndian, &common); err != nil {
		return nil, wrapSpecIdEventReadError(err)
	}

	eventData := &SpecIdEventData{
		data:             data,
		PlatformClass:    common.PlatformClass,
		SpecVersionMinor: common.SpecVersionMinor,
		SpecVersionMajor: common.SpecVersionMajor,
		SpecErrata:       common.SpecErrata,
		UintnSize:	  common.UintnSize}

	if err := helper(stream, eventData); err != nil {
		return nil, err
	}

	return eventData, nil
}

var (
	validNormalSeparatorValues = [...]uint32{0, math.MaxUint32}
)

type asciiStringEventData struct {
	data []byte
}

func (e *asciiStringEventData) String() string {
	return *(*string)(unsafe.Pointer(&e.data))
}

func (e *asciiStringEventData) Bytes() []byte {
	return e.data
}

type unknownNoActionEventData struct {
	data []byte
}

func (e *unknownNoActionEventData) String() string {
	return ""
}

func (e *unknownNoActionEventData) Bytes() []byte {
	return e.data
}

func (e *unknownNoActionEventData) Type() NoActionEventType {
	return UnknownNoActionEvent
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf
//  (section 11.3.4 "EV_NO_ACTION Event Types")
// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientSpecPlat_TPM_2p0_1p04_pub.pdf
//  (section 9.4.5 "EV_NO_ACTION Event Types")
func decodeEventDataNoAction(data []byte) (out EventData, trailingBytes int, err error) {
	stream := bytes.NewReader(data)

	// Signature field
	signature := make([]byte, 16)
	if _, err := io.ReadFull(stream, signature); err != nil {
		return nil, 0, err
	}

	switch *(*string)(unsafe.Pointer(&signature)) {
	case "Spec ID Event00\x00":
		d, e := decodeSpecIdEvent(stream, data, parsePCClientSpecIdEvent)
		if d != nil {
			out = d
		}
		err = e
	case "Spec ID Event02\x00":
		d, e := decodeSpecIdEvent(stream, data, parseEFI_1_2_SpecIdEvent)
		if d != nil {
			out = d
		}
		err = e
	case "Spec ID Event03\x00":
		d, e := decodeSpecIdEvent(stream, data, parseEFI_2_SpecIdEvent)
		if d != nil {
			out = d
		}
		err = e
	case "SP800-155 Event\x00":
		d, e := decodeBIMReferenceManifestEvent(stream, data)
		if d != nil {
			out = d
		}
		err = e
	case "StartupLocality\x00":
		d, e := decodeStartupLocalityEvent(stream, data)
		if d != nil {
			out = d
		}
		err = e
	default:
		return &unknownNoActionEventData{data}, 0, nil
	}

	return
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf (section 11.3.3 "EV_ACTION event types")
// https://trustedcomputinggroup.org/wp-content/uploads/PC-ClientSpecific_Platform_Profile_for_TPM_2p0_Systems_v51.pdf (section 9.4.3 "EV_ACTION Event Types")
func decodeEventDataAction(data []byte) (*asciiStringEventData, int, error) {
	return &asciiStringEventData{data: data}, 0, nil
}

type separatorEventData struct {
	data    []byte
	isError bool
}

func (e *separatorEventData) String() string {
	if !e.isError {
		return ""
	}
	return "*ERROR*"
}

func (e *separatorEventData) Bytes() []byte {
	return e.data
}

func decodeEventDataSeparator(data []byte, isError bool) (*separatorEventData, int, error) {
	return &separatorEventData{data: data, isError: isError}, 0, nil
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf (section 11.3.1 "Event Types")
// https://trustedcomputinggroup.org/wp-content/uploads/TCG_EFI_Platform_1_22_Final_-v15.pdf (section 7.2 "Event Types")
// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientSpecPlat_TPM_2p0_1p04_pub.pdf (section 9.4.1 "Event Types")
func decodeEventDataTCG(eventType EventType, data []byte,
	hasDigestOfSeparatorError bool) (out EventData, trailingBytes int, err error) {
	switch eventType {
	case EventTypeNoAction:
		return decodeEventDataNoAction(data)
	case EventTypeSeparator:
		return decodeEventDataSeparator(data, hasDigestOfSeparatorError)
	case EventTypeAction, EventTypeEFIAction:
		return decodeEventDataAction(data)
	case EventTypeEFIVariableDriverConfig, EventTypeEFIVariableBoot, EventTypeEFIVariableAuthority:
		return decodeEventDataEFIVariable(data, eventType)
	case EventTypeEFIBootServicesApplication, EventTypeEFIBootServicesDriver,
		EventTypeEFIRuntimeServicesDriver:
		return decodeEventDataEFIImageLoad(data)
	case EventTypeEFIGPTEvent:
		return decodeEventDataEFIGPT(data)
	default:
	}
	return nil, 0, nil
}
