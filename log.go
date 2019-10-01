package tcglog

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// LogOptions allows the behaviour of Log to be controlled.
type LogOptions struct {
	EnableGrub bool // Enable support for interpreting events recorded by GRUB
}

func isKnownAlgorithm(alg AlgorithmId) (out bool) {
	_, out = knownAlgorithms[alg]
	return
}

var zeroDigests = map[AlgorithmId][]byte{
	AlgorithmSha1:   make([]byte, knownAlgorithms[AlgorithmSha1]),
	AlgorithmSha256: make([]byte, knownAlgorithms[AlgorithmSha256]),
	AlgorithmSha384: make([]byte, knownAlgorithms[AlgorithmSha384]),
	AlgorithmSha512: make([]byte, knownAlgorithms[AlgorithmSha512])}

type stream interface {
	readNextEvent() (*Event, int, error)
}

func isPCRIndexInRange(index PCRIndex) bool {
	const maxPCRIndex PCRIndex = 31
	return index <= maxPCRIndex
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf
//  (section 3.3.2.2 2 Error Conditions" , section 8.2.3 "Measuring Boot Events")
// https://trustedcomputinggroup.org/wp-content/uploads/PC-ClientSpecific_Platform_Profile_for_TPM_2p0_Systems_v51.pdf:
//  (section 2.3.2 "Error Conditions", section 2.3.4 "PCR Usage", section 7.2
//   "Procedure for Pre-OS to OS-Present Transition")
func isDigestOfSeparatorErrorValue(digest Digest, alg AlgorithmId) bool {
	errorValue := make([]byte, 4)
	binary.LittleEndian.PutUint32(errorValue, separatorEventErrorValue)

	return bytes.Compare(digest, hashSum(errorValue, alg)) == 0
}

func wrapLogReadError(origErr error, partial bool) error {
	if origErr == io.EOF {
		if !partial {
			return origErr
		}
		origErr = io.ErrUnexpectedEOF
	}

	return fmt.Errorf("error when reading from log stream (%v)", origErr)
}

func wrapPCRIndexOutOfRangeError(pcrIndex PCRIndex) error {
	return fmt.Errorf("log entry has an out-of-range PCR index (%d)", pcrIndex)
}

type stream_1_2 struct {
	r       io.ReadSeeker
	options LogOptions
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientImplementation_1-21_1_00.pdf
//  (section 11.1.1 "TCG_PCClientPCREventStruct Structure")
func (s *stream_1_2) readNextEvent() (*Event, int, error) {
	var pcrIndex PCRIndex
	if err := binary.Read(s.r, binary.LittleEndian, &pcrIndex); err != nil {
		return nil, 0, wrapLogReadError(err, false)
	}

	if !isPCRIndexInRange(pcrIndex) {
		return nil, 0, wrapPCRIndexOutOfRangeError(pcrIndex)
	}

	var eventType EventType
	if err := binary.Read(s.r, binary.LittleEndian, &eventType); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	digest := make(Digest, knownAlgorithms[AlgorithmSha1])
	if _, err := s.r.Read(digest); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}
	digests := make(DigestMap)
	digests[AlgorithmSha1] = digest

	var eventSize uint32
	if err := binary.Read(s.r, binary.LittleEndian, &eventSize); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	event := make([]byte, eventSize)
	if _, err := io.ReadFull(s.r, event); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	data, trailing := decodeEventData(pcrIndex, eventType, event, &s.options,
		isDigestOfSeparatorErrorValue(digest, AlgorithmSha1))

	return &Event{
		PCRIndex:  pcrIndex,
		EventType: eventType,
		Digests:   digests,
		Data:      data,
	}, trailing, nil
}

type stream_2 struct {
	r              io.ReadSeeker
	options        LogOptions
	algSizes       []EFISpecIdEventAlgorithmSize
	readFirstEvent bool
}

// https://trustedcomputinggroup.org/wp-content/uploads/TCG_PCClientSpecPlat_TPM_2p0_1p04_pub.pdf
//  (section 9.2.2 "TCG_PCR_EVENT2 Structure")
func (s *stream_2) readNextEvent() (*Event, int, error) {
	if !s.readFirstEvent {
		s.readFirstEvent = true
		stream := stream_1_2{r: s.r}
		return stream.readNextEvent()
	}

	var pcrIndex PCRIndex
	if err := binary.Read(s.r, binary.LittleEndian, &pcrIndex); err != nil {
		return nil, 0, wrapLogReadError(err, false)
	}

	if !isPCRIndexInRange(pcrIndex) {
		return nil, 0, wrapPCRIndexOutOfRangeError(pcrIndex)
	}

	var eventType EventType
	if err := binary.Read(s.r, binary.LittleEndian, &eventType); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	var count uint32
	if err := binary.Read(s.r, binary.LittleEndian, &count); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	digests := make(DigestMap)

	for i := uint32(0); i < count; i++ {
		var algorithmId AlgorithmId
		if err := binary.Read(s.r, binary.LittleEndian, &algorithmId); err != nil {
			return nil, 0, wrapLogReadError(err, true)
		}

		var digestSize uint16
		var j int
		for j = 0; j < len(s.algSizes); j++ {
			if s.algSizes[j].AlgorithmId == algorithmId {
				digestSize = s.algSizes[j].DigestSize
				break
			}
		}

		if j == len(s.algSizes) {
			return nil, 0, fmt.Errorf("crypto-agile log entry contains a digest for an unrecognized "+
				"algorithm (%s)", algorithmId)
		}

		digest := make(Digest, digestSize)
		if _, err := io.ReadFull(s.r, digest); err != nil {
			return nil, 0, wrapLogReadError(err, true)
		}

		if _, exists := digests[algorithmId]; exists {
			return nil, 0, fmt.Errorf("crypto-agile log entry contains more than one digest value "+
				"for algorithm %s", algorithmId)
		}
		digests[algorithmId] = digest
	}

	for _, algSize := range s.algSizes {
		if _, exists := digests[algSize.AlgorithmId]; !exists {
			return nil, 0,
				fmt.Errorf("crypto-agile log entry is missing a digest value for algorithm %s "+
					"that was present in the Spec ID Event", algSize.AlgorithmId)
		}
	}

	for alg, _ := range digests {
		if isKnownAlgorithm(alg) {
			continue
		}
		delete(digests, alg)
	}

	var eventSize uint32
	if err := binary.Read(s.r, binary.LittleEndian, &eventSize); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	event := make([]byte, eventSize)
	if _, err := io.ReadFull(s.r, event); err != nil {
		return nil, 0, wrapLogReadError(err, true)
	}

	data, trailing := decodeEventData(pcrIndex, eventType, event, &s.options,
		isDigestOfSeparatorErrorValue(digests[s.algSizes[0].AlgorithmId], s.algSizes[0].AlgorithmId))

	return &Event{
		PCRIndex:  pcrIndex,
		EventType: eventType,
		Digests:   digests,
		Data:      data,
	}, trailing, nil
}

func fixupSpecIdEvent(event *Event, algorithms AlgorithmIdList) {
	if event.Data.(*SpecIdEventData).Spec != SpecEFI_2 {
		return
	}

	for _, alg := range algorithms {
		if alg == AlgorithmSha1 {
			continue
		}

		if _, ok := event.Digests[alg]; ok {
			continue
		}

		event.Digests[alg] = zeroDigests[alg]
	}
}

func isSpecIdEvent(event *Event) (out bool) {
	_, out = event.Data.(*SpecIdEventData)
	return
}

// Log corresponds to an event log parser instance, and allows the consumer to iterate over log entries.
type Log struct {
	Spec         Spec            // The specification to which this log conforms
	Algorithms   AlgorithmIdList // The digest algorithms that appear in the log
	stream       stream
	failed       bool
	indexTracker map[PCRIndex]uint
}

func newLogFromReader(r io.ReadSeeker, options LogOptions) (*Log, error) {
	start, err := r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, fmt.Errorf("cannot obtain current offset in stream: %v", err)
	}

	var stream stream = &stream_1_2{r: r, options: options}
	event, _, err := stream.readNextEvent()
	if err != nil {
		return nil, wrapLogReadError(err, true)
	}

	var spec Spec = SpecUnknown
	var digestSizes []EFISpecIdEventAlgorithmSize
	var algorithms AlgorithmIdList

	switch d := event.Data.(type) {
	case *SpecIdEventData:
		spec = d.Spec
		digestSizes = d.DigestSizes
	case *BrokenEventData:
		if _, isSpecErr := d.Error.(invalidSpecIdEventError); isSpecErr {
			return nil, d.Error
		}
	}

	if spec == SpecEFI_2 {
		algorithms = make(AlgorithmIdList, 0, len(digestSizes))
		for _, specAlgSize := range digestSizes {
			if isKnownAlgorithm(specAlgSize.AlgorithmId) {
				algorithms = append(algorithms, specAlgSize.AlgorithmId)
			}
		}
		stream = &stream_2{r: r,
			options:        options,
			algSizes:       digestSizes,
			readFirstEvent: false}
	} else {
		algorithms = AlgorithmIdList{AlgorithmSha1}
	}

	_, err = r.Seek(start, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("cannot return to start of log: %v", err)
	}

	return &Log{Spec: spec,
		Algorithms:   algorithms,
		stream:       stream,
		failed:       false,
		indexTracker: map[PCRIndex]uint{}}, nil
}

func (l *Log) nextEventInternal() (*Event, int, error) {
	if l.failed {
		return nil, 0,
			errors.New("cannot read next event: log status inconsistent due to a previous error")
	}

	event, trailing, err := l.stream.readNextEvent()
	if err != nil {
		if err != io.EOF {
			l.failed = true
		}
		return nil, 0, err
	}

	if i, exists := l.indexTracker[event.PCRIndex]; exists {
		event.Index = i
		l.indexTracker[event.PCRIndex] = i + 1
	} else {
		event.Index = 0
		l.indexTracker[event.PCRIndex] = 1
	}

	if isSpecIdEvent(event) {
		fixupSpecIdEvent(event, l.Algorithms)
	}

	return event, trailing, nil
}

// NextEvent returns an Event structure that corresponds to the next event in the log. Upon successful completion,
// the Log instance will advance to the next event. If there are no more events in the log, it will return io.EOF.
func (l *Log) NextEvent() (event *Event, err error) {
	event, _, err = l.nextEventInternal()
	return
}

// NewLogFromByteReader creates a new Log instance that reads a log from the provided reader.
func NewLogFromByteReader(reader *bytes.Reader, options LogOptions) (*Log, error) {
	return newLogFromReader(reader, options)
}

// NewLogFromFile creates a new Log instance that reads a log from the specified file.
func NewLogFromFile(file *os.File, options LogOptions) (*Log, error) {
	return newLogFromReader(file, options)
}
