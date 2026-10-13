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

package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/yaml"
)

// Render the actual DaemonSet so a patch cannot accidentally add a duplicate
// regular container for the existing native objectstore-plugin sidecar.
func TestNodeManagementReservationComponent(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "manifests", "ate-install")
	base, err := os.ReadFile(filepath.Join(root, "atelet.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	component, err := os.ReadFile(filepath.Join(root, "components", "node-management-reservation", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		fs := filesys.MakeFsInMemory()
		if err := fs.MkdirAll("/case/component"); err != nil {
			t.Fatal(err)
		}
		manifest := strings.ReplaceAll(string(base), "${SUBSTRATE_VERSION_SUFFIX}", "-test")
		manifest = strings.ReplaceAll(manifest, "${SUBSTRATE_VERSION}", "test")
		if err := fs.WriteFile("/case/atelet.yaml", []byte(manifest)); err != nil {
			t.Fatal(err)
		}
		config := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n- atelet.yaml\n"
		if enabled {
			config += "components:\n- component\n"
		}
		if err := fs.WriteFile("/case/kustomization.yaml", []byte(config)); err != nil {
			t.Fatal(err)
		}
		if err := fs.WriteFile("/case/component/kustomization.yaml", component); err != nil {
			t.Fatal(err)
		}
		result, err := krusty.MakeKustomizer(krusty.MakeDefaultOptions()).Run(fs, "/case")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, resource := range result.Resources() {
			if resource.GetKind() != "DaemonSet" {
				continue
			}
			found = true
			data, err := resource.AsYAML()
			if err != nil {
				t.Fatal(err)
			}
			var ds appsv1.DaemonSet
			if err := yaml.Unmarshal(data, &ds); err != nil {
				t.Fatal(err)
			}
			regular, sidecars := ds.Spec.Template.Spec.Containers, ds.Spec.Template.Spec.InitContainers
			if len(regular) != 1 || regular[0].Name != "atelet" {
				t.Fatalf("enabled=%v regular=%+v", enabled, regular)
			}
			if len(sidecars) != 1 || sidecars[0].Name != "objectstore-plugin" {
				t.Fatalf("enabled=%v init=%+v", enabled, sidecars)
			}
			want := int64(50)
			if enabled {
				want = 250
			}
			if got := regular[0].Resources.Requests.Cpu().MilliValue(); got != want {
				t.Errorf("atelet request=%d want=%d", got, want)
			}
			if got := sidecars[0].Resources.Requests.Cpu().MilliValue(); got != want {
				t.Errorf("sidecar request=%d want=%d", got, want)
			}
			if regular[0].Image == "" || sidecars[0].Image == "" {
				t.Fatal("component lost container image")
			}
			if regular[0].Resources.Limits.Cpu().Sign() != 0 || sidecars[0].Resources.Limits.Cpu().Sign() != 0 {
				t.Fatal("component introduced CPU limits")
			}
		}
		if !found {
			t.Fatal("DaemonSet missing")
		}
	}
}
