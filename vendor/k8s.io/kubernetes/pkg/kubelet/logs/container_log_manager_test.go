/*
Copyright 2026 The Kubernetes Authors.

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

package logs

import (
	"context"
	"os"
	"strings"
	"testing"

	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
	critest "k8s.io/cri-api/pkg/apis/testing"
	containertest "k8s.io/kubernetes/pkg/kubelet/container/testing"
)

func TestCleanRejectsUnsafeLogPaths(t *testing.T) {
	for _, logPath := range []string{"", ".", "boot", "/", "/etc/passwd", "/var/log/pods"} {
		t.Run(logPath, func(t *testing.T) {
			runtimeService := critest.NewFakeRuntimeService()
			runtimeService.SetFakeContainers([]*critest.FakeContainer{{
				ContainerStatus: runtimeapi.ContainerStatus{
					Id:      "unsafe-container",
					State:   runtimeapi.ContainerState_CONTAINER_EXITED,
					LogPath: logPath,
				},
			}})
			globCalls := 0
			fakeOS := &containertest.FakeOS{
				Files: map[string][]*os.FileInfo{"/bin": nil},
				GlobFn: func(_, _ string) bool {
					globCalls++
					return true
				},
			}
			manager := &containerLogManager{
				runtimeService:   runtimeService,
				osInterface:      fakeOS,
				podLogsDirectory: "/var/log/pods",
			}

			if err := manager.Clean(context.Background(), "unsafe-container"); err == nil {
				t.Fatalf("Clean() accepted unsafe log path %q", logPath)
			}
			if globCalls != 0 {
				t.Fatalf("Glob called %d times for unsafe log path %q", globCalls, logPath)
			}
			if len(fakeOS.Removes) != 0 {
				t.Fatalf("Remove called for unsafe log path %q: %v", logPath, fakeOS.Removes)
			}
		})
	}
}

func TestCleanRemovesOnlyLogsUnderPodLogsDirectory(t *testing.T) {
	const logPath = "/var/log/pods/kubeedge_edgecore_uid/container/0.log"
	runtimeService := critest.NewFakeRuntimeService()
	runtimeService.SetFakeContainers([]*critest.FakeContainer{{
		ContainerStatus: runtimeapi.ContainerStatus{
			Id:      "valid-container",
			State:   runtimeapi.ContainerState_CONTAINER_EXITED,
			LogPath: logPath,
		},
	}})
	fakeOS := &containertest.FakeOS{
		Files: map[string][]*os.FileInfo{
			logPath:                         nil,
			logPath + ".20260716-120000.gz": nil,
			"/var/log/pods/other/1.log":     nil,
		},
		GlobFn: func(pattern, path string) bool {
			return strings.HasPrefix(path, strings.TrimSuffix(pattern, "*"))
		},
	}
	manager := &containerLogManager{
		runtimeService:   runtimeService,
		osInterface:      fakeOS,
		podLogsDirectory: "/var/log/pods",
	}

	if err := manager.Clean(context.Background(), "valid-container"); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if len(fakeOS.Removes) != 2 {
		t.Fatalf("Remove calls = %v, want current and rotated log", fakeOS.Removes)
	}
	for _, removed := range fakeOS.Removes {
		if !strings.HasPrefix(removed, logPath) {
			t.Fatalf("removed path outside container logs: %q", removed)
		}
	}
}
