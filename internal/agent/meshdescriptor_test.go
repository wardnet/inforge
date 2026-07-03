package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/wardnet/inforge/internal/meshpaths"
)

const validMeshDescriptor = `
version: 1
provider:
  kind: infisical
  url: https://app.infisical.com
  project: wsid
  environment: prod
  secret_path: /bridge-01
services:
  - tenants
  - tunneller
`

func TestParseMeshDescriptor(t *testing.T) {
	d, err := ParseMeshDescriptor([]byte(validMeshDescriptor))
	if err != nil {
		t.Fatalf("ParseMeshDescriptor: %v", err)
	}
	if d.Provider.SecretPath != "/bridge-01" || len(d.Services) != 2 {
		t.Fatalf("unexpected descriptor: %+v", d)
	}
}

func TestParseMeshDescriptorRejects(t *testing.T) {
	cases := map[string]string{
		"wrong version": strings.Replace(validMeshDescriptor, "version: 1", "version: 2", 1),
		"no provider":   "version: 1\nservices: [a]\n",
		"no services":   "version: 1\nprovider: {kind: infisical}\n",
		"unknown field": validMeshDescriptor + "extra: true\n",
	}
	for name, in := range cases {
		if _, err := ParseMeshDescriptor([]byte(in)); err == nil {
			t.Errorf("%s: expected error, got none", name)
		}
	}
}

// The provider keys and the tmpfs projection paths must agree by construction:
// RuntimeDir + "/" + key == the meshpaths material path the nginx config reads.
func TestMeshFilesLandAtMeshPaths(t *testing.T) {
	d := MeshDescriptor{Services: []string{"tenants"}}
	files := d.Files()
	if len(files) != 3 {
		t.Fatalf("Files() = %v, want bundle + leaf cert/key", files)
	}
	for _, key := range files {
		dest := filepath.Join(meshpaths.RuntimeDir, filepath.FromSlash(key))
		switch key {
		case meshpaths.BundleKey:
			if dest != meshpaths.BundlePath {
				t.Errorf("bundle dest %q != BundlePath %q", dest, meshpaths.BundlePath)
			}
		case meshpaths.LeafCertKey("tenants"):
			if dest != meshpaths.LeafCertPath("tenants") {
				t.Errorf("leaf cert dest %q != LeafCertPath %q", dest, meshpaths.LeafCertPath("tenants"))
			}
		case meshpaths.LeafKeyKey("tenants"):
			if dest != meshpaths.LeafKeyPath("tenants") {
				t.Errorf("leaf key dest %q != LeafKeyPath %q", dest, meshpaths.LeafKeyPath("tenants"))
			}
		default:
			t.Errorf("unexpected key %q", key)
		}
	}
}

// A missing mesh descriptor is fail-soft: the pull must never block the mesh
// proxy from starting on whatever material is already on disk.
func TestRunMeshProjectFailSoft(t *testing.T) {
	if err := runMeshProject(t.TempDir()); err != nil {
		t.Fatalf("runMeshProject on empty dir: %v, want nil (fail-soft)", err)
	}
}
