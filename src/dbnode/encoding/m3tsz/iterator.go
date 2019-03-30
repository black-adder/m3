// Copyright (c) 2016 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package m3tsz

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"time"

	"github.com/m3db/m3/src/dbnode/encoding"
	"github.com/m3db/m3/src/dbnode/ts"
	xtime "github.com/m3db/m3x/time"
)

// readerIterator provides an interface for clients to incrementally
// read datapoints off of an encoded stream.
type ReaderIterator struct {
	is   encoding.IStream
	opts encoding.Options

	// internal bookkeeping
	tsIterator TimestampIteratorState
	vb         uint64 // current float value
	xor        uint64 // current float xor
	err        error  // current error

	intVal float64 // current int value

	mult uint8 // current int multiplier
	sig  uint8 // current number of significant bits for int diff

	intOptimized bool // whether encoding scheme is optimized for ints
	isFloat      bool // whether encoding is in int or float

	closed bool
}

// NewReaderIterator returns a new iterator for a given reader
func NewReaderIterator(reader io.Reader, is encoding.IStream, intOptimized bool, opts encoding.Options) encoding.ReaderIterator {
	if is == nil {
		// Only allocate a new IStream if one isn't provided because in some cases we want
		// to share an IStream between multiple iterators so we can reuse some of the logic.
		is = encoding.NewIStream(reader)
	}
	return &ReaderIterator{
		is:           is,
		opts:         opts,
		tsIterator:   NewTimestampIteratorState(opts),
		intOptimized: intOptimized,
	}
}

// Next moves to the next item
func (it *ReaderIterator) Next() bool {
	if !it.hasNext() {
		return false
	}

	first := it.tsIterator.ReadTimestamp(it.is)
	it.readValue(first)

	return it.hasNext()
}

func (it *ReaderIterator) readValue(first bool) {
	if first {
		it.readFirstValue()
	} else {
		it.readNextValue()
	}
}

func (it *ReaderIterator) readFirstValue() {
	if !it.intOptimized {
		it.readFullFloatVal()
		return
	}

	if it.readBits(1) == opcodeFloatMode {
		it.readFullFloatVal()
		it.isFloat = true
		return
	}

	it.readIntSigMult()
	it.readIntValDiff()
}

func (it *ReaderIterator) readNextValue() {
	if !it.intOptimized {
		it.readFloatXOR()
		return
	}

	if it.readBits(1) == opcodeUpdate {
		if it.readBits(1) == opcodeRepeat {
			return
		}

		if it.readBits(1) == opcodeFloatMode {
			// Change to floatVal
			it.readFullFloatVal()
			it.isFloat = true
			return
		}

		it.readIntSigMult()
		it.readIntValDiff()
		it.isFloat = false
		return
	}

	if it.isFloat {
		it.readFloatXOR()
	} else {
		it.readIntValDiff()
	}
}

func (it *ReaderIterator) readFullFloatVal() {
	it.vb = it.readBits(64)
	it.xor = it.vb
}

func (it *ReaderIterator) readFloatXOR() {
	it.xor = it.ReadXOR(it.xor)
	it.vb ^= it.xor
}

func (it *ReaderIterator) readIntSigMult() {
	if it.readBits(1) == opcodeUpdateSig {
		if it.readBits(1) == OpcodeZeroSig {
			it.sig = 0
		} else {
			it.sig = uint8(it.readBits(NumSigBits)) + 1
		}
	}

	if it.readBits(1) == opcodeUpdateMult {
		it.mult = uint8(it.readBits(numMultBits))
		if it.mult > maxMult {
			it.err = errInvalidMultiplier
		}
	}
}

func (it *ReaderIterator) readIntValDiff() {
	sign := -1.0
	if it.readBits(1) == opcodeNegative {
		sign = 1.0
	}

	it.intVal += sign * float64(it.readBits(int(it.sig)))
}

// ReadXOR reads the next XOR value.
func (it *ReaderIterator) ReadXOR(prevXOR uint64) uint64 {
	cb := it.readBits(1)
	if cb == opcodeZeroValueXOR {
		return 0
	}

	cb = (cb << 1) | it.readBits(1)
	if cb == opcodeContainedValueXOR {
		previousLeading, previousTrailing := encoding.LeadingAndTrailingZeros(prevXOR)
		numMeaningfulBits := 64 - previousLeading - previousTrailing
		return it.readBits(numMeaningfulBits) << uint(previousTrailing)
	}

	numLeadingZeros := int(it.readBits(6))
	numMeaningfulBits := int(it.readBits(6)) + 1
	numTrailingZeros := 64 - numLeadingZeros - numMeaningfulBits
	meaningfulBits := it.readBits(numMeaningfulBits)
	return meaningfulBits << uint(numTrailingZeros)
}

func (it *ReaderIterator) readBits(numBits int) uint64 {
	if !it.hasNext() {
		return 0
	}
	var res uint64
	res, it.err = it.is.ReadBits(numBits)
	return res
}

// Current returns the value as well as the annotation associated with the current datapoint.
// Users should not hold on to the returned Annotation object as it may get invalidated when
// the iterator calls Next().
func (it *ReaderIterator) Current() (ts.Datapoint, xtime.Unit, ts.Annotation) {
	if !it.intOptimized || it.isFloat {
		return ts.Datapoint{
			Timestamp: it.tsIterator.PrevTime,
			Value:     math.Float64frombits(it.vb),
		}, it.tsIterator.TimeUnit, it.tsIterator.PrevAnt
	}

	return ts.Datapoint{
		Timestamp: it.tsIterator.PrevTime,
		Value:     convertFromIntFloat(it.intVal, it.mult),
	}, it.tsIterator.TimeUnit, it.tsIterator.PrevAnt
}

// Err returns the error encountered
func (it *ReaderIterator) Err() error {
	return it.err
}

func (it *ReaderIterator) hasError() bool {
	return it.err != nil
}

func (it *ReaderIterator) isDone() bool {
	return it.tsIterator.Done
}

func (it *ReaderIterator) isClosed() bool {
	return it.closed
}

func (it *ReaderIterator) hasNext() bool {
	return !it.hasError() && !it.isDone() && !it.isClosed()
}

// Reset resets the ReadIterator for reuse.
func (it *ReaderIterator) Reset(reader io.Reader) {
	it.is.Reset(reader)
	it.tsIterator = NewTimestampIteratorState(it.opts)
	it.vb = 0
	it.xor = 0
	it.err = nil
	it.isFloat = false
	it.intVal = 0.0
	it.mult = 0
	it.sig = 0
	it.closed = false
}

// Close closes the ReaderIterator.
func (it *ReaderIterator) Close() {
	if it.closed {
		return
	}

	it.closed = true
	pool := it.opts.ReaderIteratorPool()
	if pool != nil {
		pool.Put(it)
	}
}

// TimestampIteratorState encapsulates all the state required for iterator over
// delta-of-delta compresed timestamps.
type TimestampIteratorState struct {
	PrevTime      time.Time
	PrevTimeDelta time.Duration
	PrevAnt       ts.Annotation

	TimeUnit        xtime.Unit
	TimeUnitChanged bool

	Done bool

	Opts encoding.Options
}

// NewTimestampIteratorState creates a new TimestampIteratorState.
func NewTimestampIteratorState(opts encoding.Options) TimestampIteratorState {
	return TimestampIteratorState{
		Opts: opts,
	}
}

// ReadTimestamp reads the first or next timestamp.
func (it *TimestampIteratorState) ReadTimestamp(stream encoding.IStream) bool {
	it.PrevAnt = nil
	it.TimeUnitChanged = false

	first := false
	if it.PrevTime.IsZero() {
		first = true
		it.readFirstTimestamp(stream)
	} else {
		it.readNextTimestamp(stream)
	}

	// NB(xichen): reset time delta to 0 when there is a time unit change to be
	// consistent with the encoder.
	if it.TimeUnitChanged {
		it.PrevTimeDelta = 0
	}

	return first
}

func (it *TimestampIteratorState) readFirstTimestamp(stream encoding.IStream) error {
	ntBits, err := stream.ReadBits(64)
	if err != nil {
		return err
	}

	nt := int64(ntBits)
	// NB(xichen): first time stamp is always normalized to nanoseconds.
	st := xtime.FromNormalizedTime(nt, time.Nanosecond)
	it.TimeUnit = initialTimeUnit(st, it.Opts.DefaultTimeUnit())

	err = it.readNextTimestamp(stream)
	if err != nil {
		return err
	}

	it.PrevTime = st.Add(it.PrevTimeDelta)
	return nil
}

func (it *TimestampIteratorState) readNextTimestamp(stream encoding.IStream) error {
	dod, err := it.readMarkerOrDeltaOfDelta(stream)
	if err != nil {
		return err
	}

	it.PrevTimeDelta += dod
	it.PrevTime = it.PrevTime.Add(it.PrevTimeDelta)
	return nil
}

func (it *TimestampIteratorState) tryReadMarker(stream encoding.IStream) (time.Duration, bool, error) {
	mes := it.Opts.MarkerEncodingScheme()
	numBits := mes.NumOpcodeBits() + mes.NumValueBits()
	opcodeAndValue, success := it.tryPeekBits(stream, numBits)
	if !success {
		return 0, false, nil
	}

	opcode := opcodeAndValue >> uint(mes.NumValueBits())
	if opcode != mes.Opcode() {
		return 0, false, nil
	}

	var (
		valueMask   = (1 << uint(mes.NumValueBits())) - 1
		markerValue = int64(opcodeAndValue & uint64(valueMask))
	)
	switch encoding.Marker(markerValue) {
	case mes.EndOfStream():
		_, err := stream.ReadBits(numBits)
		if err != nil {
			return 0, false, err
		}
		it.Done = true
		return 0, true, nil
	case mes.Annotation():
		_, err := stream.ReadBits(numBits)
		if err != nil {
			return 0, false, err
		}
		err = it.readAnnotation(stream)
		if err != nil {
			return 0, false, err
		}
		markerOrDOD, err := it.readMarkerOrDeltaOfDelta(stream)
		if err != nil {
			return 0, false, err
		}
		return markerOrDOD, true, nil
	case mes.TimeUnit():
		_, err := stream.ReadBits(numBits)
		if err != nil {
			return 0, false, err
		}
		err = it.readTimeUnit(stream)
		if err != nil {
			return 0, false, err
		}
		markerOrDOD, err := it.readMarkerOrDeltaOfDelta(stream)
		if err != nil {
			return 0, false, err
		}
		return markerOrDOD, true, nil
	default:
		return 0, false, nil
	}
}

func (it *TimestampIteratorState) readMarkerOrDeltaOfDelta(stream encoding.IStream) (time.Duration, error) {
	dod, success, err := it.tryReadMarker(stream)
	if err != nil {
		return 0, err
	}

	if success {
		return dod, nil
	}

	tes, exists := it.Opts.TimeEncodingSchemes()[it.TimeUnit]
	if !exists {
		return 0, fmt.Errorf("time encoding scheme for time unit %v doesn't exist", it.TimeUnit)
	}

	return it.readDeltaOfDelta(stream, tes)
}

func (it *TimestampIteratorState) readDeltaOfDelta(
	stream encoding.IStream, tes encoding.TimeEncodingScheme) (time.Duration, error) {
	if it.TimeUnitChanged {
		// NB(xichen): if the time unit has changed, always read 64 bits as normalized
		// dod in nanoseconds.
		dodBits, err := stream.ReadBits(64)
		if err != nil {
			return 0, err
		}

		dod := encoding.SignExtend(dodBits, 64)
		return time.Duration(dod), nil
	}

	cb, err := stream.ReadBits(1)
	if err != nil {
		return 0, err
	}
	if cb == tes.ZeroBucket().Opcode() {
		return 0, nil
	}

	buckets := tes.Buckets()
	for i := 0; i < len(buckets); i++ {
		nextCB, err := stream.ReadBits(1)
		if err != nil {
			return 0, nil
		}

		cb = (cb << 1) | nextCB
		if cb == buckets[i].Opcode() {
			dodBits, err := stream.ReadBits(buckets[i].NumValueBits())
			if err != nil {
				return 0, err
			}

			dod := encoding.SignExtend(dodBits, buckets[i].NumValueBits())
			timeUnit, err := it.TimeUnit.Value()
			if err != nil {
				return 0, nil
			}

			return xtime.FromNormalizedDuration(dod, timeUnit), nil
		}
	}

	numValueBits := tes.DefaultBucket().NumValueBits()
	dodBits, err := stream.ReadBits(numValueBits)
	if err != nil {
		return 0, err
	}

	dod := encoding.SignExtend(dodBits, numValueBits)
	timeUnit, err := it.TimeUnit.Value()
	if err != nil {
		return 0, nil
	}

	return xtime.FromNormalizedDuration(dod, timeUnit), nil
}

func (it *TimestampIteratorState) readAnnotation(stream encoding.IStream) error {
	antLen, err := it.readVarint(stream)
	if err != nil {
		return err
	}

	// NB: we add 1 here to offset the 1 we subtracted during encoding.
	antLen = antLen + 1
	if antLen <= 0 {
		return fmt.Errorf("unexpected annotation length %d", antLen)
	}

	// TODO(xichen): use pool to allocate the buffer once the pool diff lands.
	buf := make([]byte, antLen)
	n, err := stream.Read(buf)
	if err != nil {
		return err
	}
	if n != antLen {
		return fmt.Errorf(
			"expected to read %d annotation bytes but read: %d",
			antLen, n)
	}
	it.PrevAnt = buf

	return nil
}

func (it *TimestampIteratorState) readTimeUnit(stream encoding.IStream) error {
	tuBits, err := stream.ReadBits(8)
	if err != nil {
		return err
	}

	tu := xtime.Unit(tuBits)
	if tu.IsValid() && tu != it.TimeUnit {
		it.TimeUnitChanged = true
	}
	it.TimeUnit = tu

	return nil
}

func (it *TimestampIteratorState) readVarint(stream encoding.IStream) (int, error) {
	res, err := binary.ReadVarint(stream)
	return int(res), err
}

func (it *TimestampIteratorState) tryPeekBits(stream encoding.IStream, numBits int) (uint64, bool) {
	res, err := stream.PeekBits(numBits)
	if err != nil {
		return 0, false
	}
	return res, true
}
