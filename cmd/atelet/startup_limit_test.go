// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeStartupLimitDisabledAndInvalid(t *testing.T) {
	if _, err := makeStartupSlots(-1); err == nil {
		t.Fatal("negative startup limit accepted")
	}
	slots, err := makeStartupSlots(0)
	if err != nil || slots != nil {
		t.Fatal("default no longer unbounded")
	}
	s := &AteomHerder{}
	done, err := s.waitStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done()
}
func TestNodeStartupLimitConcurrentAndCancellation(t *testing.T) {
	slots, err := makeStartupSlots(2)
	if err != nil {
		t.Fatal(err)
	}
	s := &AteomHerder{startupSlots: slots}
	done1, _ := s.waitStartup(context.Background())
	done2, _ := s.waitStartup(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.waitStartup(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel error=%v", err)
	}
	done1()
	done2()
	var active, peak atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Go(func() {
			done, err := s.waitStartup(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			defer done()
			n := active.Add(1)
			defer active.Add(-1)
			for p := peak.Load(); n > p; p = peak.Load() {
				if peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
		})
	}
	wg.Wait()
	if peak.Load() > 2 || peak.Load() == 0 || len(slots) != 0 {
		t.Fatalf("peak=%d tokens=%d", peak.Load(), len(slots))
	}
}
func TestNodeStartupLimitAlreadyCanceledWithCapacity(t *testing.T) {
	slots, _ := makeStartupSlots(2)
	s := &AteomHerder{startupSlots: slots}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 20 {
		if _, err := s.waitStartup(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("canceled startup admitted")
		}
	}
	if len(slots) != 0 {
		t.Fatal("cancellation leaked startup slot")
	}
}
