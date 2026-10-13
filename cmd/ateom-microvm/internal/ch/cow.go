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

package ch

import (
	"encoding/json"
	"fmt"
	"slices"
)

const (
	MemRestoreCopyOnWrite = "CopyOnWrite"
	// CoWStrictCapability is advertised by the independently pinned VMM patch.
	// Unlike upstream's CopyOnWrite API it rejects unsupported layouts instead
	// of silently copying. A version string alone cannot establish this contract.
	CoWStrictCapability = "restore-cow-strict-v1"
	RestoreLegacy       = "legacy"
	RestoreCoWPreferred = "cow-preferred"
	RestoreCoWRequired  = "cow-required"
)

type RestoreDecision struct {
	Mode           string
	FallbackReason string
}

func ValidateRestorePolicy(policy string) error {
	switch policy {
	case RestoreLegacy, RestoreCoWPreferred, RestoreCoWRequired:
		return nil
	}
	return fmt.Errorf("unknown memory restore policy %q (want legacy, cow-preferred, or cow-required)", policy)
}

// SelectRestoreMode checks actual VMM capability and snapshot memory/device
// layout before an explicit CoW request reaches the VMM. Malformed snapshots
// fail even in preferred mode; only a supported legacy path may be a fallback.
func SelectRestoreMode(info VMMInfo, policy, legacy string, config []byte) (RestoreDecision, error) {
	if err := ValidateRestorePolicy(policy); err != nil {
		return RestoreDecision{}, err
	}
	if policy == RestoreLegacy {
		return RestoreDecision{Mode: legacy}, nil
	}
	var cfg struct {
		Memory *struct {
			Shared         bool              `json:"shared"`
			Hugepages      bool              `json:"hugepages"`
			THP            *bool             `json:"thp"`
			Mergeable      bool              `json:"mergeable"`
			Zones          []json.RawMessage `json:"zones"`
			HotplugSize    uint64            `json:"hotplug_size"`
			HotpluggedSize uint64            `json:"hotplugged_size"`
		} `json:"memory"`
		FS               []json.RawMessage `json:"fs"`
		GenericVhostUser []json.RawMessage `json:"generic_vhost_user"`
		Devices          []json.RawMessage `json:"devices"`
		UserDevices      []json.RawMessage `json:"user_devices"`
		VDPA             []json.RawMessage `json:"vdpa"`
		NUMA             []json.RawMessage `json:"numa"`
		Disks            []struct {
			VhostUser bool `json:"vhost_user"`
		} `json:"disks"`
		Net []struct {
			VhostUser bool `json:"vhost_user"`
		} `json:"net"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return RestoreDecision{}, fmt.Errorf("decoding CoW snapshot config: %w", err)
	}
	if cfg.Memory == nil {
		return RestoreDecision{}, fmt.Errorf("CoW snapshot config lacks memory settings")
	}
	reason := ""
	switch {
	case cfg.Memory.Shared || len(cfg.FS) > 0 || len(cfg.GenericVhostUser) > 0:
		reason = "shared memfd/vhost-user memory cannot use private CoW mappings"
	case cfg.Memory.Hugepages:
		reason = "hugepage memory is outside the CoW compatibility subset"
	case cfg.Memory.THP == nil || *cfg.Memory.THP:
		reason = "CoW requires explicit thp=false to preserve memory policy"
	case len(cfg.Memory.Zones) > 0 || len(cfg.NUMA) > 0:
		reason = "memory zones/NUMA policy are outside the CoW compatibility subset"
	case cfg.Memory.HotplugSize > 0 || cfg.Memory.HotpluggedSize > 0:
		reason = "hotplug memory is outside the CoW compatibility subset"
	case cfg.Memory.Mergeable:
		reason = "mergeable memory is outside the CoW compatibility subset"
	case len(cfg.Devices) > 0 || len(cfg.UserDevices) > 0 || len(cfg.VDPA) > 0:
		reason = "passthrough devices are outside the CoW compatibility subset"
	}
	for _, d := range cfg.Disks {
		if d.VhostUser {
			reason = "vhost-user disk requires shared memory"
		}
	}
	for _, n := range cfg.Net {
		if n.VhostUser {
			reason = "vhost-user network requires shared memory"
		}
	}
	if reason == "" && !slices.Contains(info.Features, CoWStrictCapability) {
		reason = "VMM does not advertise " + CoWStrictCapability
	}
	if reason != "" {
		if policy == RestoreCoWRequired {
			return RestoreDecision{}, fmt.Errorf("required mmap CoW restore unavailable: %s", reason)
		}
		return RestoreDecision{Mode: legacy, FallbackReason: reason}, nil
	}
	return RestoreDecision{Mode: MemRestoreCopyOnWrite}, nil
}
