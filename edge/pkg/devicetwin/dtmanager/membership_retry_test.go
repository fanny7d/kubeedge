package dtmanager

import (
	"errors"
	"sync"
	"testing"

	"github.com/kubeedge/kubeedge/edge/pkg/devicetwin/dtcommon"
	"github.com/kubeedge/kubeedge/edge/pkg/devicetwin/dtcontext"
	"github.com/kubeedge/kubeedge/edge/pkg/devicetwin/dttype"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao/mocks"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao/models"
)

type membershipFailureService struct {
	*mocks.MockDeviceService
	add func([]models.Device, []models.DeviceAttr, []models.DeviceTwin) error
}

func (s *membershipFailureService) AddDeviceTrans(devices []models.Device, attributes []models.DeviceAttr, twins []models.DeviceTwin) error {
	return s.add(devices, attributes, twins)
}

func TestAddDeviceDatabaseFailureContinuesMembership(t *testing.T) {
	for _, delta := range []bool{false, true} {
		name := "initial-sync"
		if delta {
			name = "delta-update"
		}
		t.Run(name, func(t *testing.T) {
			original := MembershipServiceFactory
			t.Cleanup(func() { MembershipServiceFactory = original })
			calls := make(map[string]int)
			service := &membershipFailureService{MockDeviceService: mocks.NewMockDeviceService()}
			service.add = func(devices []models.Device, _ []models.DeviceAttr, _ []models.DeviceTwin) error {
				id := devices[0].ID
				calls[id]++
				if id == "failed" {
					return errors.New("database is locked")
				}
				return nil
			}
			MembershipServiceFactory = func() interface {
				AddDeviceTrans([]models.Device, []models.DeviceAttr, []models.DeviceTwin) error
				DeleteDeviceTrans([]string) error
				QueryDevice(string, string) ([]models.Device, error)
				QueryDeviceAttr(string, string) (*[]models.DeviceAttr, error)
				QueryDeviceTwin(string, string) (*[]models.DeviceTwin, error)
			} {
				return service
			}
			context := &dtcontext.DTContext{
				DeviceList: &sync.Map{}, DeviceMutex: &sync.Map{}, Mutex: &sync.RWMutex{},
				CommChan: make(map[string]chan interface{}), NodeName: "test-node",
			}
			addDevice(context, []dttype.Device{{ID: "failed"}, {ID: "next"}}, dttype.BaseMessage{}, delta)
			if calls["failed"] != dtcommon.RetryTimes || calls["next"] != 1 {
				t.Fatalf("membership did not retain bounded retries and process the next device: %v", calls)
			}
			if _, exists := context.GetDevice("failed"); exists {
				t.Fatal("failed database write retained an in-memory device")
			}
			if _, exists := context.GetDevice("next"); !exists {
				t.Fatal("membership stopped before the next device")
			}
			for _, id := range []string{"failed", "next"} {
				value, _ := context.DeviceMutex.Load(id)
				mutex := value.(*sync.Mutex)
				if !mutex.TryLock() {
					t.Fatalf("device %s retained a locked mutex", id)
				}
				mutex.Unlock()
			}
		})
	}
}
