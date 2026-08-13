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

package edgestream

import (
	"testing"
	"time"
)

func TestEdgeStreamReconnectBackoff(t *testing.T) {
	backoff := newEdgeStreamReconnectBackoff(0)
	want := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
		30 * time.Second,
	}
	for i, expected := range want {
		if got := backoff.Step(); got != expected {
			t.Fatalf("step %d: expected %s, got %s", i, expected, got)
		}
	}
}

func TestResetEdgeStreamReconnectBackoff(t *testing.T) {
	t.Run("short session retains accumulated delay", func(t *testing.T) {
		backoff := newEdgeStreamReconnectBackoff(0)
		_ = backoff.Step()
		_ = backoff.Step()
		if resetEdgeStreamReconnectBackoff(&backoff, edgeStreamStableSessionDuration-time.Second) {
			t.Fatal("short session unexpectedly reset reconnect backoff")
		}
		if got := backoff.Step(); got != 8*time.Second {
			t.Fatalf("expected accumulated 8s delay, got %s", got)
		}
	})

	t.Run("stable session resets to initial delay", func(t *testing.T) {
		backoff := newEdgeStreamReconnectBackoff(0)
		_ = backoff.Step()
		_ = backoff.Step()
		if !resetEdgeStreamReconnectBackoff(&backoff, edgeStreamStableSessionDuration) {
			t.Fatal("stable session did not reset reconnect backoff")
		}
		if got := backoff.Step(); got != edgeStreamInitialReconnectDelay {
			t.Fatalf("expected initial %s delay, got %s", edgeStreamInitialReconnectDelay, got)
		}
	})
}

func TestWaitForEdgeStreamReconnect(t *testing.T) {
	t.Run("stops before a long retry delay", func(t *testing.T) {
		stop := make(chan struct{})
		close(stop)
		result := make(chan bool, 1)
		go func() {
			result <- waitForEdgeStreamReconnect(stop, time.Hour)
		}()
		select {
		case allowed := <-result:
			if allowed {
				t.Fatal("closed stop channel allowed another reconnect")
			}
		case <-time.After(time.Second):
			t.Fatal("closed stop channel did not cancel retry delay")
		}
	})

	t.Run("allows retry after delay", func(t *testing.T) {
		stop := make(chan struct{})
		if !waitForEdgeStreamReconnect(stop, time.Millisecond) {
			t.Fatal("retry delay unexpectedly stopped")
		}
	})
}
