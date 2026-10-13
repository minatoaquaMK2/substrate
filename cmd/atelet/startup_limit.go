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
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/pflag"
)

var maxConcurrentStartups = pflag.Int("max-concurrent-startups", 0, "Node-local maximum concurrent Actor Run/Restore preparations across all Workers. 0 preserves unbounded startup concurrency; CPU quota and scheduling are unchanged.")

func makeStartupSlots(limit int) (chan struct{}, error) {
	if limit < 0 {
		return nil, fmt.Errorf("--max-concurrent-startups must not be negative")
	}
	if limit == 0 {
		return nil, nil
	}
	return make(chan struct{}, limit), nil
}

// waitStartup bounds expensive image/snapshot preparation as well as the VMM
// RPC, while leaving checkpoint, shutdown, health and stats operations ungated.
func (s *AteomHerder) waitStartup(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.startupSlots == nil {
		return func() {}, nil
	}
	start := time.Now()
	select {
	case s.startupSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.startupSlots
			return nil, err
		}
		slog.InfoContext(ctx, "Node startup admitted", slog.Duration("resourceWait", time.Since(start)), slog.Int("startupLimit", cap(s.startupSlots)))
		return func() { <-s.startupSlots }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for node startup capacity: %w", ctx.Err())
	}
}
