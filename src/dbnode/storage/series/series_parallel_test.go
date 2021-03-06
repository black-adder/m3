// +build big
//
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

package series

import (
	"sync"
	"testing"
	"time"

	"github.com/m3db/m3x/context"
	"github.com/m3db/m3x/ident"
	xtime "github.com/m3db/m3x/time"

	"github.com/stretchr/testify/assert"
)

// TestSeriesWriteReadParallel is a regression test that was added to capture panics that might
// arise when many parallel writes and reads are happening at the same time.
func TestSeriesWriteReadParallel(t *testing.T) {
	var (
		numWorkers        = 100
		numStepsPerWorker = numWorkers * 100
		opts              = newSeriesTestOptions()
		curr              = time.Now()
		series            = NewDatabaseSeries(ident.StringID("foo"), ident.Tags{}, opts).(*dbSeries)
	)

	_, err := series.Bootstrap(nil)
	assert.NoError(t, err)

	ctx := context.NewContext()
	defer ctx.Close()

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		for i := 0; i < numStepsPerWorker; i++ {
			wasWritten, err := series.Write(
				ctx, curr.Add(time.Duration(i)*time.Nanosecond), float64(i), xtime.Second, nil)
			if err != nil {
				panic(err)
			}
			if !wasWritten {
				panic("write failed")
			}
		}
		wg.Done()
	}()

	// Outer loop so that reads are competing with other reads, not just writes.
	for j := 0; j < numWorkers; j++ {
		wg.Add(1)
		go func() {
			for i := 0; i < numStepsPerWorker; i++ {
				_, err := series.ReadEncoded(ctx, curr.Add(-5*time.Minute), curr.Add(time.Minute))
				if err != nil {
					panic(err)
				}
			}
			wg.Done()
		}()
	}
	wg.Wait()
}
