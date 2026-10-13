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
	"strings"
	"testing"
)

func TestCoWRestoreSelection(t *testing.T) {
	capable := VMMInfo{Version: "53.0.0", Features: []string{CoWStrictCapability}}
	compatible := `{"memory":{"size":4096,"shared":false,"thp":false}}`
	tests := []struct {
		name, config, reason string
		info                 VMMInfo
	}{
		{"v53", compatible, "VMM does not advertise", VMMInfo{Version: "53.0.0"}},
		{"upstream future is not capability", compatible, "VMM does not advertise", VMMInfo{Version: "999.0.0"}},
		{"shared", `{"memory":{"shared":true,"thp":false}}`, "shared memfd", capable},
		{"virtiofs", `{"memory":{"thp":false},"fs":[{}]}`, "vhost-user", capable},
		{"hugepage", `{"memory":{"hugepages":true,"thp":false}}`, "hugepage", capable},
		{"implicit THP", `{"memory":{}}`, "thp=false", capable},
		{"zones", `{"memory":{"thp":false,"zones":[{}]}}`, "zones", capable},
		{"numa", `{"memory":{"thp":false},"numa":[{}]}`, "NUMA", capable},
		{"hotplug", `{"memory":{"thp":false,"hotplug_size":4096}}`, "hotplug", capable},
		{"KSM", `{"memory":{"thp":false,"mergeable":true}}`, "mergeable", capable},
		{"passthrough", `{"memory":{"thp":false},"devices":[{}]}`, "passthrough", capable},
		{"vhost disk", `{"memory":{"thp":false},"disks":[{"vhost_user":true}]}`, "vhost-user disk", capable},
		{"vhost net", `{"memory":{"thp":false},"net":[{"vhost_user":true}]}`, "vhost-user network", capable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := SelectRestoreMode(tt.info, RestoreCoWPreferred, MemRestoreEager, []byte(tt.config))
			if err != nil || d.Mode != MemRestoreEager || !strings.Contains(d.FallbackReason, tt.reason) {
				t.Fatalf("preferred: %+v %v", d, err)
			}
			if _, err := SelectRestoreMode(tt.info, RestoreCoWRequired, MemRestoreEager, []byte(tt.config)); err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("required: %v", err)
			}
		})
	}
	d, err := SelectRestoreMode(capable, RestoreCoWRequired, MemRestoreEager, []byte(compatible))
	if err != nil || d.Mode != MemRestoreCopyOnWrite || d.FallbackReason != "" {
		t.Fatalf("compatible: %+v %v", d, err)
	}
	// Default-off path never inspects or changes the snapshot or version policy.
	d, err = SelectRestoreMode(VMMInfo{}, RestoreLegacy, MemRestoreOnDemand, nil)
	if err != nil || d.Mode != MemRestoreOnDemand {
		t.Fatalf("legacy: %+v %v", d, err)
	}
	for _, cfg := range []string{"{", `{}`, `{"memory":"bad"}`} {
		if _, err := SelectRestoreMode(capable, RestoreCoWPreferred, MemRestoreEager, []byte(cfg)); err == nil {
			t.Fatalf("malformed snapshot %s accepted", cfg)
		}
	}
	if _, err := SelectRestoreMode(capable, "typo", MemRestoreEager, nil); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
