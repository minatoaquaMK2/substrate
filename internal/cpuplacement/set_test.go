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

package cpuplacement

import (
	"strconv"
	"strings"
	"testing"
)

func set(t testing.TB, value string) Set {
	t.Helper()
	s, err := ParseSet(value)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestParseSetBoundsOverlappingExpansion(t *testing.T) {
	input := strings.Repeat("0-1023,", 1024) + "0-1023"
	if _, err := ParseSet(input); err == nil || !strings.Contains(err.Error(), "expansion") {
		t.Fatal("overlapping ranges bypassed total expansion limit", err)
	}
}

func TestParseSet(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"\n", ""}, {"0", "0"}, {"4,2,0-1,2,9-11", "0-2,4,9-11"},
		{" 2-4,03,7\n", "2-4,7"}, {"4096,1,65536", "1,4096,65536"},
		{strconv.Itoa(int(^uint(0) >> 1)), strconv.Itoa(int(^uint(0) >> 1))},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := set(t, tc.input).String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	for _, input := range []string{"-1", "+1", "3-1", "1,,2", "1,", ",1", "0-", "1-2-3", "1 2", "0, 1", "a", "0-1048576", "999999999999999999999999999"} {
		t.Run("invalid_"+input, func(t *testing.T) {
			if _, err := ParseSet(input); err == nil {
				t.Errorf("accepted %q", input)
			}
		})
	}
}

func TestSetOperationsAndImmutability(t *testing.T) {
	a, b := set(t, "0,2-4,8"), set(t, "1-2,4,9")
	if got := a.Intersection(b).String(); got != "2,4" {
		t.Errorf("intersection %s", got)
	}
	if got := a.Difference(b).String(); got != "0,3,8" {
		t.Errorf("difference %s", got)
	}
	if got := a.Union(b).String(); got != "0-4,8-9" {
		t.Errorf("union %s", got)
	}
	if !a.IsSubset(a.Union(b)) || a.IsSubset(b) || !a.Equal(set(t, "8,4,3,2,0")) {
		t.Fatal("subset or equality")
	}
	input := []int{9, 1, 9}
	s, err := NewSet(input...)
	if err != nil {
		t.Fatal(err)
	}
	input[1] = 99
	ids := s.IDs()
	ids[0] = 99
	if s.String() != "1,9" || !s.Contains(9) || s.Contains(99) || s.Len() != 2 {
		t.Fatal("mutable or incorrect set")
	}
	if _, err := NewSet(-1); err == nil {
		t.Fatal("accepted negative ID")
	}
}

func FuzzParseSet(f *testing.F) {
	for _, input := range []string{"", "0-3,8", "9,1,1", "2-1", "1,,2", "0-2147483647"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		s, err := ParseSet(input)
		if err != nil {
			return
		}
		roundTrip, err := ParseSet(s.String())
		if err != nil || !s.Equal(roundTrip) {
			t.Fatalf("round trip %q: %v", input, err)
		}
		if !s.Intersection(s).Equal(s) || s.Difference(s).Len() != 0 {
			t.Fatal("set invariants")
		}
	})
}
