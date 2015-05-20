package scheduler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/testapi"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/cache"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/record"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/runtime"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/watch"

	log "github.com/golang/glog"
	mesos "github.com/mesos/mesos-go/mesosproto"
	util "github.com/mesos/mesos-go/mesosutil"
	bindings "github.com/mesos/mesos-go/scheduler"
	"github.com/mesosphere/kubernetes-mesos/pkg/queue"
	schedcfg "github.com/mesosphere/kubernetes-mesos/pkg/scheduler/config"
	"github.com/mesosphere/kubernetes-mesos/pkg/scheduler/ha"
	"github.com/mesosphere/kubernetes-mesos/pkg/scheduler/podtask"
	"github.com/stretchr/testify/assert"
)

func makeTestServer(t *testing.T, namespace string, pods *api.PodList) (*httptest.Server) {
	podsHandler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(runtime.EncodeOrDie(testapi.Codec(), pods)))
	}

	mux := http.NewServeMux()
	mux.Handle(testapi.ResourcePath("pods", namespace, ""), http.HandlerFunc(podsHandler))
	mux.HandleFunc("/", func(res http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected request: %v", req.RequestURI)
		res.WriteHeader(http.StatusNotFound)
	})
	return httptest.NewServer(mux)
}

func TestPlugin_New(t *testing.T) {
	assert := assert.New(t)

	c := PluginConfig{}
	p := NewPlugin(&c)
	assert.NotNil(p)
}

// Create mock of pods ListWatch, usually listening on the apiserver pods watch endpoint
type MockPodsListWatch struct {
	ListWatch cache.ListWatch
	fakeWatcher *watch.FakeWatcher
	list api.PodList
}
func NewMockPodsListWatch(initialPodList api.PodList) *MockPodsListWatch {
	lw := MockPodsListWatch{
		fakeWatcher: watch.NewFake(),
		list: initialPodList,
	}
	lw.ListWatch = cache.ListWatch{
		WatchFunc: func (resourceVersion string) (watch.Interface, error) {
			return lw.fakeWatcher, nil
		},
		ListFunc: func() (runtime.Object, error) {
			return &lw.list, nil
		},
	}
	return &lw
}
func (lw *MockPodsListWatch) Add(pod *api.Pod) {
	lw.list.Items = append(lw.list.Items, *pod)
	lw.fakeWatcher.Add(pod)
}
func (lw *MockPodsListWatch) Modify(pod *api.Pod) {
	for i, otherPod := range lw.list.Items {
		if otherPod.Name == pod.Name {
			lw.list.Items[i] = *pod
			lw.fakeWatcher.Modify(pod)
			return
		}
	}
	log.Fatalf("Cannot find pod %v to modify in MockPodsListWatch", pod.Name)
}
func (lw *MockPodsListWatch) Delete(pod *api.Pod) {
	for i, otherPod := range lw.list.Items {
		if otherPod.Name == pod.Name {
			lw.list.Items = append(lw.list.Items[:i], lw.list.Items[i+1:]...)
			lw.fakeWatcher.Delete(&otherPod)
			return
		}
	}
	log.Fatalf("Cannot find pod %v to delete in MockPodsListWatch", pod.Name)
}

func NewTestPod(i int) *api.Pod {
	name := fmt.Sprintf("pod%d", i)
	return &api.Pod{
		TypeMeta:   api.TypeMeta{APIVersion: testapi.Version()},
		ObjectMeta: api.ObjectMeta{
			Name: name,
			Namespace: "default",
			SelfLink: fmt.Sprintf("http://1.2.3.4/api/v1beta1/pods/%v", i),
		},
		Spec: api.PodSpec{
			Containers: []api.Container{{Ports: []api.ContainerPort{}}},
		},
		Status: api.PodStatus{
			PodIP: fmt.Sprintf("1.2.3.%d", 4+i),
			Conditions: []api.PodCondition{
				{
					Type:   api.PodReady,
					Status: api.ConditionTrue,
				},
			},
		},
	}
}

func NewTestOffer(i int) *mesos.Offer {
	hostname := fmt.Sprintf("h%d", i)
	cpus := util.NewScalarResource("cpus", 3.75)
	mem := util.NewScalarResource("mem", 940)
	return  &mesos.Offer{
		Id: util.NewOfferID(fmt.Sprintf("offer%d", i)),
		Hostname: &hostname,
		SlaveId: util.NewSlaveID(hostname),
		Resources: []*mesos.Resource{cpus, mem},
	}
}

// Add assertions to reason about event streams
type EventPredicate func (e *api.Event) bool
type EventAssertions struct {
	assert.Assertions
}
func (a *EventAssertions) Event(pred EventPredicate, msgAndArgs ...interface{}) bool {
	// parse msgAndArgs: first possibly a duration, otherwise a format string with further args
	timeout := time.Second * 2
	msg := "event not received"
	msgArgStart := 0
	if len(msgAndArgs) > 0 {
		switch msgAndArgs[0].(type) {
			case time.Duration:
				timeout = msgAndArgs[0].(time.Duration)
				msgArgStart += 1
		}
	}
	if len(msgAndArgs) > msgArgStart {
		msg = fmt.Sprintf(msgAndArgs[msgArgStart].(string), msgAndArgs[msgArgStart + 1:]...)
	}

	// watch events
	result := make(chan struct{})
	matched := false
	eventWatch := record.GetEvents(func(e *api.Event) {
		if matched { return }
		if pred(e) {
			log.V(3).Infof("found asserted event for reason '%v': %v", e.Reason, e.Message)
			matched = true
			result <- struct{}{}
		} else {
			log.V(5).Infof("ignoring not-asserted event for reason '%v': %v", e.Reason, e.Message)
		}
	})
	defer eventWatch.Stop()

	// wait for watch to match or timeout
	select {
	case <-result:
		return true
	case <-time.After(timeout):
		return a.Fail(msg)
	}
}
func (a *EventAssertions) EventWithReason(reason string, msgAndArgs ...interface{}) bool {
	return a.Event(func (e *api.Event) bool {
		return e.Reason == reason
	}, msgAndArgs...)
}

// Extend the MockSchedulerDriver with a blocking Join method
type StatusMockSchedulerDriver struct {
	MockSchedulerDriver
	stopped chan struct{}
	aborted chan struct{}
	status mesos.Status
}
func (m *StatusMockSchedulerDriver) Start() (mesos.Status, error) {
	if m.status != mesos.Status_DRIVER_NOT_STARTED {
		return m.status, errors.New("cannot start driver which isn't in status NOT_STARTED")
	}
	m.status = mesos.Status_DRIVER_RUNNING
	return m.status, nil
}
func (m *StatusMockSchedulerDriver) Stop(b bool) (mesos.Status, error) {
	close(m.stopped)
	m.status = mesos.Status_DRIVER_STOPPED
	return m.status, nil
}
func (m *StatusMockSchedulerDriver) Abort() (mesos.Status, error) {
	close(m.aborted)
	m.status = mesos.Status_DRIVER_ABORTED
	return m.status, nil
}
func (m *StatusMockSchedulerDriver) Join() (mesos.Status, error) {
	select {
	case <-m.stopped:
		log.Info("JoinableMockSchedulerDriver stopped")
		return mesos.Status_DRIVER_STOPPED, nil
	case <-m.aborted:
		log.Info("JoinableMockSchedulerDriver aborted")
		return mesos.Status_DRIVER_ABORTED, nil
	}
	return mesos.Status_DRIVER_ABORTED, errors.New("unknown reason for join")
}
func (m *StatusMockSchedulerDriver) ReconcileTasks(statuses []*mesos.TaskStatus) (mesos.Status, error) {
	return m.status, nil
}
func (m *StatusMockSchedulerDriver) LaunchTasks(offerIds []*mesos.OfferID, ti []*mesos.TaskInfo, f *mesos.Filters) (mesos.Status, error) {
	return m.status, nil
}

func TestPlugin_NewFromScheduler(t *testing.T) {
	assert := &EventAssertions{*assert.New(t)}

	// create a fake pod watch. We use that below to submit new pods to the scheduler
	podListWatch := NewMockPodsListWatch(api.PodList{})

	// create fake apiserver
	testApiServer := makeTestServer(t, api.NamespaceDefault, &podListWatch.list)
	defer testApiServer.Close()

	// create scheduler
	testScheduler := New(Config{
		Executor: util.NewExecutorInfo(
			util.NewExecutorID("executor-id"),
			util.NewCommandInfo("executor-cmd"),
		),
		Client: client.NewOrDie(&client.Config{Host: testApiServer.URL, Version: testapi.Version()}),
		PodsListWatch: &podListWatch.ListWatch,
		ScheduleFunc: FCFSScheduleFunc,
		Schedcfg: *schedcfg.CreateDefaultConfig(),
	})

	assert.NotNil(testScheduler.client, "client is nil")
	assert.NotNil(testScheduler.executor, "executor is nil")
	assert.NotNil(testScheduler.offers, "offer registry is nil")

	// create scheduler process
	schedulerProcess := ha.New(testScheduler)

	// get plugin config from it
	c := testScheduler.NewPluginConfig(schedulerProcess.Terminal(), http.DefaultServeMux)
	assert.NotNil(c)

	// create plugin
	p := NewPlugin(c)
	assert.NotNil(p)

	// run plugin
	p.Run(schedulerProcess.Terminal())
	defer schedulerProcess.End()

	// init scheduler
	err := testScheduler.Init(schedulerProcess.Master(), p, http.DefaultServeMux)
	assert.NoError(err)

	// create mock mesos scheduler driver
	mockDriver := StatusMockSchedulerDriver{
		status: mesos.Status_DRIVER_NOT_STARTED,
	}

	// tell scheduler to be registered
	testScheduler.Registered(
		&mockDriver,
		util.NewFrameworkID("kubernetes-id"),
		util.NewMasterInfo("master-id", (192 << 24) + (168 << 16) + (0 << 8) + 1, 5050),
	)

	// elect master with mock driver
	driverFactory := ha.DriverFactory(func() (bindings.SchedulerDriver, error) {
		return &mockDriver, nil;
	})
	schedulerProcess.Elect(driverFactory)
	elected := schedulerProcess.Elected()

	// wait for being elected
	_ = <-elected

	// fake new, unscheduled pod
	pod1 := NewTestPod(1)
	podListWatch.Add(pod1)

	// wait for failedScheduling event because there is no offer
	assert.EventWithReason("failedScheduling", "failedScheduling event not received")

	// add some matching offer
	offers1 := []*mesos.Offer{NewTestOffer(1)}
	testScheduler.ResourceOffers(nil, offers1)

	// and wait for scheduled pod
	assert.EventWithReason("scheduled")

}

func TestDeleteOne_NonexistentPod(t *testing.T) {
	assert := assert.New(t)
	obj := &MockScheduler{}
	reg := podtask.NewInMemoryRegistry()
	obj.On("tasks").Return(reg)

	qr := newQueuer(nil)
	assert.Equal(0, len(qr.podQueue.List()))
	d := &deleter{
		api: obj,
		qr:  qr,
	}
	pod := &Pod{Pod: &api.Pod{
		ObjectMeta: api.ObjectMeta{
			Name:      "foo",
			Namespace: api.NamespaceDefault,
		}}}
	err := d.deleteOne(pod)
	assert.Equal(err, noSuchPodErr)
	obj.AssertExpectations(t)
}

func TestDeleteOne_PendingPod(t *testing.T) {
	assert := assert.New(t)
	obj := &MockScheduler{}
	reg := podtask.NewInMemoryRegistry()
	obj.On("tasks").Return(reg)

	pod := &Pod{Pod: &api.Pod{
		ObjectMeta: api.ObjectMeta{
			Name:      "foo",
			UID:       "foo0",
			Namespace: api.NamespaceDefault,
		}}}
	_, err := reg.Register(podtask.New(api.NewDefaultContext(), "bar", *pod.Pod, &mesos.ExecutorInfo{}))
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// preconditions
	qr := newQueuer(nil)
	qr.podQueue.Add(pod, queue.ReplaceExisting)
	assert.Equal(1, len(qr.podQueue.List()))
	_, found := qr.podQueue.Get("default/foo")
	assert.True(found)

	// exec & post conditions
	d := &deleter{
		api: obj,
		qr:  qr,
	}
	err = d.deleteOne(pod)
	assert.Nil(err)
	_, found = qr.podQueue.Get("foo0")
	assert.False(found)
	assert.Equal(0, len(qr.podQueue.List()))
	obj.AssertExpectations(t)
}

func TestDeleteOne_Running(t *testing.T) {
	assert := assert.New(t)
	obj := &MockScheduler{}
	reg := podtask.NewInMemoryRegistry()
	obj.On("tasks").Return(reg)

	pod := &Pod{Pod: &api.Pod{
		ObjectMeta: api.ObjectMeta{
			Name:      "foo",
			UID:       "foo0",
			Namespace: api.NamespaceDefault,
		}}}
	task, err := reg.Register(podtask.New(api.NewDefaultContext(), "bar", *pod.Pod, &mesos.ExecutorInfo{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	task.Set(podtask.Launched)
	err = reg.Update(task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// preconditions
	qr := newQueuer(nil)
	qr.podQueue.Add(pod, queue.ReplaceExisting)
	assert.Equal(1, len(qr.podQueue.List()))
	_, found := qr.podQueue.Get("default/foo")
	assert.True(found)

	obj.On("killTask", task.ID).Return(nil)

	// exec & post conditions
	d := &deleter{
		api: obj,
		qr:  qr,
	}
	err = d.deleteOne(pod)
	assert.Nil(err)
	_, found = qr.podQueue.Get("foo0")
	assert.False(found)
	assert.Equal(0, len(qr.podQueue.List()))
	obj.AssertExpectations(t)
}

func TestDeleteOne_badPodNaming(t *testing.T) {
	assert := assert.New(t)
	obj := &MockScheduler{}
	pod := &Pod{Pod: &api.Pod{}}
	d := &deleter{
		api: obj,
		qr:  newQueuer(nil),
	}

	err := d.deleteOne(pod)
	assert.NotNil(err)

	pod.Pod.ObjectMeta.Name = "foo"
	err = d.deleteOne(pod)
	assert.NotNil(err)

	pod.Pod.ObjectMeta.Name = ""
	pod.Pod.ObjectMeta.Namespace = "bar"
	err = d.deleteOne(pod)
	assert.NotNil(err)

	obj.AssertExpectations(t)
}
