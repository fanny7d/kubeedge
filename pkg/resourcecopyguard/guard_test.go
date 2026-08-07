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

package resourcecopyguard

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResourceCopyGuard(t *testing.T) {
	root := t.TempDir()
	stateDirectory, err := Create(root, "copy-id")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	now := time.Now()
	if !IsActive(root, "edgecore", "kubeedge", "copy-id", now) {
		t.Fatal("fresh KubeEdge resource-copy guard was not honored")
	}
	if IsActive(root, "workload", "kubeedge", "copy-id", now) {
		t.Fatal("ordinary runtime pod must not be protected by a copy guard")
	}

	activePath := filepath.Join(stateDirectory, activeFile)
	staleTime := now.Add(-maxAge - time.Second)
	if err := os.Chtimes(activePath, staleTime, staleTime); err != nil {
		t.Fatalf("age active marker: %v", err)
	}
	if IsActive(root, "edgecore", "kubeedge", "copy-id", now) {
		t.Fatal("stale KubeEdge resource-copy guard was honored")
	}
	if _, err := os.Stat(stateDirectory); !os.IsNotExist(err) {
		t.Fatalf("stale state directory was not removed: %v", err)
	}
}

func TestCreateRejectsUnsafeUID(t *testing.T) {
	if _, err := Create(t.TempDir(), "../escape"); err == nil {
		t.Fatal("Create() accepted an unsafe sandbox UID")
	}
}
