package imitator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/storage"
	"k8s.io/klog/v2"

	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/common/constants"
	"github.com/kubeedge/kubeedge/edge/pkg/common/modules"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao/dbclient"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/dao/models"
	"github.com/kubeedge/kubeedge/edge/pkg/metamanager/metaserver/kubernetes/storage/sqlite/imitator/watchhook"
	"github.com/kubeedge/kubeedge/pkg/metaserver"
)

// imitator is a storage based on metav2 that imitate the behavior of etcd
type imitator struct {
	lock sync.RWMutex
	// The Revision is the current revision of client
	// It is set when client inits or a bigger resourceversion obj was saved into meta_v2
	revision uint64
	// to parse obj resource version from string to int64
	versioner storage.Versioner
	// to co/decoder obj
	codec runtime.Codec
}

// Inject transform the message to watch.event, save internal obj/objs to table meta_v2
// and trigger the corresponding hook to serve watch
func (s *imitator) Inject(msg model.Message) {
	for _, e := range s.Event(&msg) {
		// save to meta_v2
		var err error
		served := true
		switch e.Type {
		case watch.Added, watch.Modified:
			err = s.InsertOrUpdateObj(context.TODO(), e.Object)
		case watch.Deleted:
			served, err = s.deleteObj(context.TODO(), e.Object)
		}
		if err != nil {
			key := metaserver.KeyFunc(e.Object)
			klog.Errorf("failed to serve event {type:%v,key:%v}: %v", e.Type, key, err)
			continue
		}
		if !served {
			continue
		}
		// TODO: move Trigger inside InsertOrUpdateObj and DeleteObj
		watchhook.Trigger(e)
	}
}

// TODO: filter out insert or update req that the obj's rev is smaller than the stored
func (s *imitator) InsertOrUpdateObj(_ context.Context, obj runtime.Object) error {
	key, err := metaserver.KeyFuncObj(obj)
	if err != nil {
		return err
	}
	gvr, ns, name := metaserver.ParseKey(key)
	unstr, isUnstr := obj.(*unstructured.Unstructured)
	if !isUnstr {
		return fmt.Errorf("obj is not unstructured type")
	}
	buf := bytes.NewBuffer(nil)
	err = s.codec.Encode(unstr, buf)
	if err != nil {
		return err
	}
	objRv, err := s.versioner.ObjectResourceVersion(obj)
	m := models.MetaV2{
		Key:                  key,
		GroupVersionResource: gvr.String(),
		Namespace:            ns,
		Name:                 name,
		ResourceVersion:      objRv,
		Value:                buf.String(),
	}
	return s.insertOrReplaceMetaV2(m, objRv)
}

func (s *imitator) insertOrReplaceMetaV2(m models.MetaV2, objRv uint64) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	err := dbclient.NewMetaV2Service().RetryInsertOrReplaceMetaV2(&m, 3)
	if err != nil {
		klog.Errorf("failed to access database after retries: %v", err)
		return err
	}

	if objRv > s.GetRevision() {
		s.SetRevision(objRv)
	}
	klog.V(4).Infof("[metaserver]successfully insert or update obj:%v", m.Key)
	return nil
}

func (s *imitator) GetPassThroughObj(_ context.Context, key string) ([]byte, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	result, err := dbclient.NewMetaV2Service().GetByKey(key)
	if err != nil {
		return nil, err
	}
	klog.V(4).Infof("[metaserver]successfully queried obj:%v", key)
	return []byte(result.Value), nil
}

func (s *imitator) Delete(_ context.Context, key string) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	err := dbclient.NewMetaV2Service().DeleteByKey(key)
	if err != nil {
		klog.Errorf("[imitator] delete error: %v", err)
	}
	return err
}

func (s *imitator) InsertOrUpdatePassThroughObj(_ context.Context, obj []byte, key string) error {
	m := models.MetaV2{
		Key:   key,
		Value: string(obj),
	}
	return s.insertOrReplaceMetaV2(m, 0)
}

func (s *imitator) DeleteObj(_ context.Context, obj runtime.Object) error {
	_, err := s.deleteObj(context.TODO(), obj)
	return err
}

// deleteObj deletes only the exact object incarnation represented by obj. A
// delayed delete must not remove a newer object that reused the same key.
// The boolean reports whether a row was actually removed so Inject can avoid
// emitting duplicate or UID-mismatched watch events.
func (s *imitator) deleteObj(_ context.Context, obj runtime.Object) (bool, error) {
	key, err := metaserver.KeyFuncObj(obj)
	if err != nil {
		return false, err
	}

	expected, err := meta.Accessor(obj)
	if err != nil {
		return false, err
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	row, found, err := dbclient.NewMetaV2Service().FindByKey(key)
	if err != nil {
		return false, err
	}
	if !found {
		klog.V(4).Infof("[metaserver] skip delete for absent obj:%v", key)
		return false, nil
	}

	stored := new(unstructured.Unstructured)
	if err := runtime.DecodeInto(s.codec, []byte(row.Value), stored); err != nil {
		return false, fmt.Errorf("decode stored obj %s: %w", key, err)
	}
	expectedUID := expected.GetUID()
	if stored.GetUID() != expectedUID {
		klog.V(4).Infof("[metaserver] skip stale delete for obj:%v, expected UID:%s, stored UID:%s", key, expectedUID, stored.GetUID())
		return false, nil
	}

	if err := dbclient.NewMetaV2Service().DeleteByKey(key); err != nil {
		return false, err
	}
	return true, nil
}

func (s *imitator) Get(_ context.Context, key string) (Resp, error) {
	var resp Resp
	s.lock.RLock()
	results, err := dbclient.NewMetaV2Service().RawMetaByGVRNN(metaserver.ParseKey(key))
	resp.Revision = s.revision
	s.lock.RUnlock()
	if err != nil {
		return Resp{}, err
	}
	switch {
	case len(*results) == 1:
		resp.Kvs = results
		return resp, nil
	default:
		return Resp{}, fmt.Errorf("the server could not find the requested resource")
	}
}
func (s *imitator) List(_ context.Context, key string) (Resp, error) {
	gvr, ns, name := metaserver.ParseKey(key)
	//if name != NullName {
	//	return Resp{}, fmt.Errorf("dao client list must not have resource name")
	//}
	klog.Infof("%v,%v,%v", gvr, ns, name)
	var resp Resp
	s.lock.RLock()
	results, err := dbclient.NewMetaV2Service().RawMetaByGVRNN(gvr, ns, name)
	resp.Revision = s.revision

	s.lock.RUnlock()
	if err != nil {
		return Resp{}, err
	}
	resp.Kvs = results
	return resp, nil
}

func (s *imitator) GetRevision() uint64 {
	return s.revision
}

func (s *imitator) SetRevision(version interface{}) {
	switch v := version.(type) {
	case int64:
		s.revision = uint64(v)
	case uint64:
		s.revision = v
	case string:
		rv, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			klog.Error(err)
			return
		}
		s.revision = rv
	default:
		klog.Error("unsupported type when parse version")
	}
}

func (s *imitator) Watch(ctx context.Context, key string, rev uint64) <-chan watch.Event {
	wch := make(chan watch.Event)
	receiver := watchhook.NewChanReceiver(wch)
	wh, err := watchhook.NewWatchHook(key, rev, receiver)
	if err != nil {
		klog.Errorf("add hook for %s failed, %v", key, err)
		return nil
	}

	go func() {
		<-ctx.Done()
		wh.Stop()
		close(wch)
	}()
	return wch
}

// Event transform the message to watch.event
func (s *imitator) Event(msg *model.Message) []watch.Event {
	klog.V(4).Infof("[metaserver] get a message from metamanager: %+v", msg)
	var ret []watch.Event
	_, resType, _ := parseResource(msg.Router.Resource)
	// Edged Node messages are requests sent to the Kubernetes API, not
	// authoritative watch events. The Node object produced by kubelet does not
	// carry TypeMeta, and the accepted object is delivered from the cloud
	// separately if MetaServer needs to observe it.
	if resType == model.ResourceTypeNode && msg.GetSource() == modules.EdgedModuleName {
		klog.V(4).Info("skip edge-originated node request")
		return []watch.Event{}
	}
	// ServiceAccountAccess is an internal authorization snapshot consumed from
	// MetaManager's legacy meta table. PolicyController intentionally sends it
	// without TypeMeta, so it is not a Kubernetes watch event for MetaServer.
	if resType == model.ResourceTypeSaAccess {
		klog.V(4).Info("skip service account access snapshot")
		return []watch.Event{}
	}
	// Edged sends Pod DeleteOptions to request deletion from the Kubernetes API.
	// It is not an authoritative resource event and has no apiVersion or kind.
	// The resulting cloud-side Pod deletion is delivered separately with the
	// actual Pod object, which is the event MetaServer must persist and publish.
	if resType == model.ResourceTypePod && msg.GetOperation() == model.DeleteOperation && msg.GetSource() == modules.EdgedModuleName {
		klog.V(4).Info("skip edge-originated pod deletion request")
		return []watch.Event{}
	}
	//skip nodestatus, podstatus and node-lease
	if strings.Contains(resType, "status") || (strings.Contains(resType, "lease") && msg.GetSource() == modules.EdgedModuleName) {
		klog.V(4).Infof("skip status or node-lease messages")
		return []watch.Event{}
	}
	var bytes []byte
	var err error
	var body = msg.GetContent()
	// convert body to bytes
	switch body := body.(type) {
	case []byte:
		bytes = body
	default:
		bytes, err = json.Marshal(body)
		if err != nil {
			klog.Errorf("failed to marshal msg content, err: %+v", err)
			return ret
		}
	}
	var op watch.EventType
	switch msg.Router.Operation {
	case model.InsertOperation:
		op = watch.Added
	case model.UpdateOperation:
		op = watch.Modified
	case model.DeleteOperation:
		op = watch.Deleted
	}
	//TODO: support array List like []obj
	obj := new(unstructured.Unstructured)
	err = runtime.DecodeInto(s.codec, bytes, obj)
	if err != nil {
		klog.Errorf("failed to unmarshal message content to unstructured obj: %+v", err)
		return ret
	}
	if obj.IsList() {
		fn := func(object runtime.Object) error {
			event := watch.Event{
				Type:   op,
				Object: object,
			}
			ret = append(ret, event)
			return nil
		}
		err := obj.EachListItem(fn)
		if err != nil {
			klog.Errorf("failed to get ret list, err: %+v", err)
			return ret
		}
	} else {
		ret = append(ret, watch.Event{Type: op, Object: obj})
	}
	return ret
}

// Resource format: <namespace>/<restype>[/resid]
// return <reskey, restype, resid>
func parseResource(resource string) (string, string, string) {
	tokens := strings.Split(resource, constants.ResourceSep)
	resType := ""
	resID := ""
	switch len(tokens) {
	case 2:
		resType = tokens[len(tokens)-1]
	case 3:
		resType = tokens[len(tokens)-2]
		resID = tokens[len(tokens)-1]
	default:
	}
	return resource, resType, resID
}
