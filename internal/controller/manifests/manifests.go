package manifests

import (
	"path/filepath"

	mf "github.com/manifestival/manifestival"
)

func GetResourcesManifests(manifestConfigDir string) (mf.Manifest, error) {
	scc, err := mf.NewManifest(filepath.Join(manifestConfigDir, "rbac-ocp", "instaslice-operator-scc.yaml"))
	if err != nil {
		return scc, err
	}
	clusterRole, err := mf.NewManifest(filepath.Join(manifestConfigDir, "rbac-ocp", "openshift_cluster_role.yaml"))
	if err != nil {
		return scc, err
	}
	roleBinding, err := mf.NewManifest(filepath.Join(manifestConfigDir, "rbac-ocp", "openshift_scc_cluster_role_binding.yaml"))
	if err != nil {
		return scc, err
	}
	return scc.Append(clusterRole, roleBinding), nil
}
