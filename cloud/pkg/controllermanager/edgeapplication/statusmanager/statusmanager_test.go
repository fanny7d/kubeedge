package statusmanager

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type listTrackingClient struct {
	client.Client
	listCalls chan struct{}
}

func (c *listTrackingClient) List(context.Context, client.ObjectList, ...client.ListOption) error {
	select {
	case c.listCalls <- struct{}{}:
	default:
	}
	return nil
}

func TestWatchControllersGCWaitsForManagerCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cacheSynced := make(chan struct{})
	listCalls := make(chan struct{}, 1)
	managerStopped := make(chan struct{})
	statusManager := &statusManager{
		ctx:      ctx,
		client:   &listTrackingClient{listCalls: listCalls},
		watching: make(map[schema.GroupVersionKind]context.CancelFunc),
		waitForCacheSync: func(ctx context.Context) bool {
			select {
			case <-cacheSynced:
				return true
			case <-ctx.Done():
				return false
			}
		},
	}

	go func() {
		statusManager.runWatchControllersGC()
		close(managerStopped)
	}()

	select {
	case <-listCalls:
		t.Fatal("status controller GC listed EdgeApplications before the manager cache synced")
	case <-time.After(100 * time.Millisecond):
	}

	close(cacheSynced)
	select {
	case <-listCalls:
	case <-time.After(time.Second):
		t.Fatal("status controller GC did not start after the manager cache synced")
	}

	cancel()
	select {
	case <-managerStopped:
	case <-time.After(time.Second):
		t.Fatal("status controller GC did not stop after context cancellation")
	}
}
