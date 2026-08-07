/*
Copyright 2026 The KubeEdge Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package resourcecopyguard coordinates KubeEdge's short-lived CRI resource
// copy sandbox with the embedded kubelet's orphan-pod cleanup loop.
package resourcecopyguard

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kubeedge/api/apis/common/constants"
)

const (
	stateRootName = "resource-copy"
	activeFile    = "active"
	maxAge        = 5 * time.Minute
	copyPodName   = "edgecore"
	copyNamespace = "kubeedge"
)

// DefaultStateRoot returns the host directory shared by EdgeCore and keadm.
func DefaultStateRoot() string {
	// Do not use os.TempDir here: keadm may be launched by a login shell while
	// EdgeCore runs under systemd, and their TMPDIR values need not match.
	return filepath.Join(constants.KubeEdgePath, stateRootName)
}

// Create creates an active guard for a CRI sandbox UID and returns its state
// directory. The directory can also be mounted into the copy container for a
// completion marker.
func Create(root, uid string) (string, error) {
	if uid == "" || filepath.Base(uid) != uid || uid == "." {
		return "", fmt.Errorf("invalid resource-copy sandbox UID %q", uid)
	}
	stateDirectory := filepath.Join(root, uid)
	if err := os.MkdirAll(stateDirectory, 0700); err != nil {
		return "", fmt.Errorf("create resource-copy state directory: %w", err)
	}
	if err := os.Chmod(stateDirectory, 0700); err != nil {
		_ = os.RemoveAll(stateDirectory)
		return "", fmt.Errorf("secure resource-copy state directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, activeFile), nil, 0600); err != nil {
		_ = os.RemoveAll(stateDirectory)
		return "", fmt.Errorf("create resource-copy active marker: %w", err)
	}
	return stateDirectory, nil
}

// IsActive reports whether a runtime-only pod is a currently guarded KubeEdge
// resource-copy sandbox. Stale guards are removed so a crashed copy cannot
// leak an orphan sandbox indefinitely.
func IsActive(root, name, namespace, uid string, now time.Time) bool {
	if name != copyPodName || namespace != copyNamespace || uid == "" || filepath.Base(uid) != uid {
		return false
	}
	stateDirectory := filepath.Join(root, uid)
	info, err := os.Stat(filepath.Join(stateDirectory, activeFile))
	if err != nil {
		return false
	}
	age := now.Sub(info.ModTime())
	if age >= 0 && age <= maxAge {
		return true
	}
	_ = os.RemoveAll(stateDirectory)
	return false
}
