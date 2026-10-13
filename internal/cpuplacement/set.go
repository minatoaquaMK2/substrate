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

// Package cpuplacement allocates logical CPUs within a worker's effective
// boundary. It does not grant node-wide exclusivity or change CPU bandwidth.
package cpuplacement

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Limit total expansion work, including overlapping ranges, without limiting
// sparse CPU IDs.
const maxSetSize = 1 << 20

// Set is an immutable, sorted set of nonnegative CPU or NUMA node IDs. The zero
// value is empty. Copies share read-only storage; IDs returns a defensive copy.
type Set struct {
	ids []int
}

// NewSet constructs a set, rejecting negative IDs and deduplicating values.
func NewSet(ids ...int) (Set, error) {
	if len(ids) > maxSetSize {
		return Set{}, fmt.Errorf("set exceeds %d IDs", maxSetSize)
	}
	ids = slices.Clone(ids)
	for _, id := range ids {
		if id < 0 {
			return Set{}, fmt.Errorf("negative ID %d", id)
		}
	}
	slices.Sort(ids)
	return Set{ids: slices.Compact(ids)}, nil
}

// ParseSet parses Linux cpulist syntax, for example "0-2,7,11-12". An empty
// string is an empty set. Whitespace is accepted only around the whole value.
func ParseSet(value string) (Set, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Set{}, nil
	}
	var ranges [][2]int
	expanded := 0
	for _, part := range strings.Split(value, ",") {
		ends := strings.Split(part, "-")
		if len(ends) > 2 {
			return Set{}, fmt.Errorf("invalid set range %q", part)
		}
		lo, err := parseID(ends[0])
		if err != nil {
			return Set{}, err
		}
		hi := lo
		if len(ends) == 2 {
			hi, err = parseID(ends[1])
			if err != nil {
				return Set{}, err
			}
		}
		if hi < lo || hi-lo >= maxSetSize {
			return Set{}, fmt.Errorf("invalid or excessive set range %q", part)
		}
		width := hi - lo + 1
		if expanded > maxSetSize-width {
			return Set{}, fmt.Errorf("set expansion exceeds %d IDs", maxSetSize)
		}
		expanded += width
		ranges = append(ranges, [2]int{lo, hi})
	}
	seen := make(map[int]struct{})
	for _, bounds := range ranges {
		lo, hi := bounds[0], bounds[1]
		for id := lo; ; id++ {
			seen[id] = struct{}{}
			if id == hi {
				break
			}
		}
	}
	ids := make([]int, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return Set{ids: ids}, nil
}

func parseID(value string) (int, error) {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return 0, fmt.Errorf("invalid ID %q", value)
	}
	id, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid ID %q: %w", value, err)
	}
	return id, nil
}

func (s Set) Len() int   { return len(s.ids) }
func (s Set) IDs() []int { return slices.Clone(s.ids) }
func (s Set) Contains(id int) bool {
	_, found := slices.BinarySearch(s.ids, id)
	return found
}

// String returns a canonical Linux range list.
func (s Set) String() string {
	parts := make([]string, 0, len(s.ids))
	for i := 0; i < len(s.ids); i++ {
		start := s.ids[i]
		end := start
		for i+1 < len(s.ids) && s.ids[i+1]-end == 1 {
			i++
			end = s.ids[i]
		}
		part := strconv.Itoa(start)
		if end != start {
			part += "-" + strconv.Itoa(end)
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ",")
}

func (s Set) Intersection(other Set) Set {
	ids := make([]int, 0)
	for _, id := range s.ids {
		if other.Contains(id) {
			ids = append(ids, id)
		}
	}
	return Set{ids: ids}
}

func (s Set) Difference(other Set) Set {
	ids := make([]int, 0)
	for _, id := range s.ids {
		if !other.Contains(id) {
			ids = append(ids, id)
		}
	}
	return Set{ids: ids}
}

func (s Set) Union(other Set) Set {
	ids := append(s.IDs(), other.ids...)
	slices.Sort(ids)
	return Set{ids: slices.Compact(ids)}
}

func (s Set) IsSubset(other Set) bool {
	for _, id := range s.ids {
		if !other.Contains(id) {
			return false
		}
	}
	return true
}

func (s Set) Equal(other Set) bool { return slices.Equal(s.ids, other.ids) }
