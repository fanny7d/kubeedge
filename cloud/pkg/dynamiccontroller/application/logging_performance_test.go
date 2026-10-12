package application

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/klog/v2"

	"github.com/kubeedge/beehive/pkg/core/model"
	"github.com/kubeedge/kubeedge/cloud/pkg/dynamiccontroller/config"
	"github.com/kubeedge/kubeedge/pkg/metaserver"
)

type loggingResponseLayer struct {
	mockMessageLayer
	count  int
	status metaserver.ApplicationStatus
}

func (m *loggingResponseLayer) Response(msg model.Message) error {
	m.count++
	m.status = msg.GetContent().(*metaserver.Application).Status
	return nil
}

func applicationLoggingFixture(tb testing.TB, missing bool) (*Center, model.Message, *loggingResponseLayer) {
	tb.Helper()
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]interface{}{"name": "logging-test", "namespace": "default"},
	}}
	dc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), obj)
	ml := &loggingResponseLayer{}
	c := &Center{dynamicClient: dc, messageLayer: ml}
	name := "logging-test"
	if missing {
		name = "missing"
	}
	app := &metaserver.Application{ID: "logging-test", Nodename: "edge-test", Key: "/apps/v1/deployments/default/" + name, Verb: metaserver.Get, Option: []byte(`{}`)}
	body, err := json.Marshal(app)
	if err != nil {
		tb.Fatal(err)
	}
	msg := model.NewMessage("logging-test").BuildRouter("dynamiccontroller", "resource", "edge-test/default/pods", "get").FillBody(body)
	return c, *msg, ml
}

func configureApplicationTestLogging(tb testing.TB, output io.Writer) *flag.FlagSet {
	tb.Helper()
	fs := flag.NewFlagSet("application-logging", flag.ContinueOnError)
	klog.InitFlags(fs)
	saved := map[string]string{}
	for _, name := range []string{"v", "logtostderr", "alsologtostderr", "stderrthreshold"} {
		saved[name] = fs.Lookup(name).Value.String()
	}
	for name, value := range map[string]string{"v": "0", "logtostderr": "false", "alsologtostderr": "false", "stderrthreshold": "FATAL"} {
		if err := fs.Set(name, value); err != nil {
			tb.Fatal(err)
		}
	}
	klog.SetOutput(output)
	originalAuthorization := config.Config.EnableAuthorization
	config.Config.EnableAuthorization = false
	tb.Cleanup(func() {
		klog.Flush()
		for name, value := range saved {
			if err := fs.Set(name, value); err != nil {
				tb.Errorf("restore %s: %v", name, err)
			}
		}
		klog.SetOutput(os.Stderr)
		config.Config.EnableAuthorization = originalAuthorization
	})
	return fs
}

func TestApplicationSuccessLoggingLevelsAndErrorVisibility(t *testing.T) {
	var output bytes.Buffer
	fs := configureApplicationTestLogging(t, &output)
	for _, level := range []string{"0", "4"} {
		if err := fs.Set("v", level); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		c, msg, ml := applicationLoggingFixture(t, false)
		c.Process(msg)
		klog.Flush()
		if ml.count != 1 || ml.status != metaserver.Approved {
			t.Fatalf("v=%s response count=%d status=%s", level, ml.count, ml.status)
		}
		for _, marker := range []string{"get a Application", "successfully to process Application"} {
			if strings.Contains(output.String(), marker) != (level == "4") {
				t.Fatalf("v=%s marker %q: %s", level, marker, output.String())
			}
		}
	}
	if err := fs.Set("v", "0"); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	c, msg, ml := applicationLoggingFixture(t, true)
	c.Process(msg)
	klog.Flush()
	if ml.count != 1 || ml.status != metaserver.Rejected || !strings.Contains(output.String(), "failed to process Application") {
		t.Fatalf("error response/log: count=%d status=%s logs=%s", ml.count, ml.status, output.String())
	}
}

func BenchmarkApplicationProcessLogging(b *testing.B) {
	configureApplicationTestLogging(b, io.Discard)
	c, msg, ml := applicationLoggingFixture(b, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Process(msg)
	}
	b.StopTimer()
	if ml.count != b.N || ml.status != metaserver.Approved {
		b.Fatalf("responses=%d status=%s", ml.count, ml.status)
	}
}
