package controller

import (
	"encoding/json"
	"github.com/jarcoal/httpmock"
	"io"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"log"
	"net/http"
	"sample-controller/pkg/kubeapi"
	"strings"
	"testing"
)

func getClient(t *testing.T) (*kubeapi.KubeClient, *httpmock.MockTransport) {
	server := httpmock.NewMockTransport()
	client, err := kubeapi.NewClient("", server)
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func addPipeResponder(server *httpmock.MockTransport, path string) io.Writer {
	r, w := io.Pipe()
	responder := httpmock.ResponderFromResponse(&http.Response{StatusCode: 200, Body: r})
	server.RegisterResponder("GET", path, responder)
	return w
}

type testRateLimiter struct {
	ask  chan struct{}
	tick chan struct{}
}

func (rl *testRateLimiter) AskTick() {
	rl.ask <- struct{}{}
}

func (rl *testRateLimiter) GetChan() <-chan struct{} {
	return rl.tick
}

func (rl *testRateLimiter) Stop() {
}

func runTestController(client *kubeapi.KubeClient) *Controller {
	rl := &testRateLimiter{make(chan struct{}), make(chan struct{})}
	return NewController(client, rl, "default")
}

func TestCreationError(t *testing.T) {
	client, server := getClient(t)
	// FIXME: Add some helper functions

	controller := runTestController(client)
	err := <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		if !strings.HasPrefix(err.Error(), "Could not add CRD: Watch failed: Get ") {
			t.Error("wrong error", err.Error())
		}
		if !strings.HasSuffix(err.Error(), "no responder found") {
			t.Error("wrong error", err.Error())
		}
	}

	server.RegisterNoResponder(httpmock.NewStringResponder(404, "dummy"))
	stopController(t, controller)
	controller = runTestController(client)
	err = <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		expected := "Could not add CRD: http request failed: code=404 body=\"dummy\""
		if err.Error() != expected {
			t.Error("wrong error", err.Error())
		}
	}

	server.RegisterNoResponder(httpmock.NewStringResponder(201, "dummy"))
	stopController(t, controller)
	controller = runTestController(client)
	err = <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		expected := "Could not add CRD: Could not decode WatchEvent"
		if !strings.HasPrefix(err.Error(), expected) {
			t.Error("wrong error", err.Error())
		}
	}

	server.RegisterNoResponder(httpmock.NewStringResponder(201, `{"type": "XYZ"}`))
	stopController(t, controller)
	controller = runTestController(client)
	err = <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		expected := "Could not add CRD: Invalid EventType: XYZ"
		if !strings.HasPrefix(err.Error(), expected) {
			t.Error("wrong error", err.Error())
		}
	}

	server.RegisterNoResponder(httpmock.NewStringResponder(201, `{"type": "ADDED"}`))
	stopController(t, controller)
	controller = runTestController(client)
	err = <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		expected := "Could not add CRD: Unmarshaling of resource failed"
		if !strings.HasPrefix(err.Error(), expected) {
			t.Error("wrong error", err.Error())
		}
	}
	stopController(t, controller)

	server.RegisterNoResponder(httpmock.NewStringResponder(201,
		`{
			"type": "DELETED",
			"object": {}
		}`))
	controller = runTestController(client)
	err = <-controller.Errors
	if err == nil {
		t.Error("expected error")
	} else {
		expected := "Could not add CRD: Could not decode WatchEvent"
		if !strings.HasPrefix(err.Error(), expected) {
			t.Error("wrong error", err.Error())
		}
	}
	stopController(t, controller)
}

// FIXME: Create a struct for the return
func startTestController(t *testing.T) (*Controller,
	*httpmock.MockTransport, io.Writer, io.Writer) {
	client, server := getClient(t)

	server.RegisterNoResponder(httpmock.NewNotFoundResponder(t.Fatal))

	json := `{
			"type": "ADDED",
			"object": {
				"status": {
					"conditions": [{
						"type": "Established",
						"status":"True"
					}]
				}
                        }
		 }`

	server.RegisterResponder("POST", "/apis/apiextensions.k8s.io/v1/customresourcedefinitions", httpmock.NewStringResponder(201, ""))
	// FIXME: convert all users of =~ to use fixed path, or at least start with ^
	server.RegisterResponder("GET", "=~apiextensions.k8s.io/v1/customresourcedefinitions.*",
		httpmock.NewStringResponder(200, json))

	foos := addPipeResponder(server, "=~samplecontroller.example.com/v1alpha1/namespaces/default/foos.*")
	deployments := addPipeResponder(server, "=~apps/v1/namespaces/default/deployments.*")
	controller := runTestController(client)

	return controller, server, foos, deployments
}

func stopController(t *testing.T, c *Controller) {
	c.RequestStop()
	for err := range c.Errors {
		t.Errorf("unxpected error %s", err)
	}
}

func TestBrokenFoo(t *testing.T) {
	controller, _, foos, _ := startTestController(t)

	foos.Write([]byte("broken"))
	if err := <-controller.Errors; err == nil {
		t.Error("expected error")
	} else {
		if !strings.HasPrefix(err.Error(), "Reading Foos: Could not decode WatchEvent") {
			t.Error("wrong error", err.Error())
		}
	}

	stopController(t, controller)
}

func TestBrokenDeployment(t *testing.T) {
	controller, _, _, deployments := startTestController(t)

	deployments.Write([]byte("broken"))
	if err := <-controller.Errors; err == nil {
		t.Error("expected error")
	} else {
		if !strings.HasPrefix(err.Error(),
			"Reading deployments: Could not decode WatchEvent") {
			t.Error("wrong error", err.Error())
		}
	}

	stopController(t, controller)
}

func marshal(t *testing.T, Type string, obj interface{}) []byte {
	data, err := json.Marshal(obj)
	if err != nil {
		t.Fatal("Marhsal failed", err)
	}
	we := metav1.WatchEvent{
		Type:   Type,
		Object: runtime.RawExtension{Raw: data},
	}
	data, err = json.Marshal(&we)
	if err != nil {
		t.Fatal("Marhsal failed", err)
	}
	return data
}

func TestFoo(t *testing.T) {
	r, w := io.Pipe()
	log.SetOutput(w)
	var buf [1024]byte

	controller, server, foos, deployments := startTestController(t)
	rl := controller.rl.(*testRateLimiter)

	foo := Foo{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "abc",
			Namespace: "xyz",
			UID:       "2a198646-da46-417a-be53-b8cd5fcfbdda",
		},
		Spec: FooSpec{
			DeploymentName: "bar",
			Replicas:       1,
		},
	}

	deployment := appsv1.Deployment{}
	deploymentOK := make(chan struct{})

	checkDeployment := func(req *http.Request) (*http.Response, error) {
		data, err := ioutil.ReadAll(req.Body)
		if err != nil {
			t.Fatal("Could not read request body: ", err)
		}
		err = json.Unmarshal(data, &deployment)
		if err != nil {
			t.Fatal("Could not unmarshal deployment: ", err)
		}
		if deployment.Namespace != foo.Namespace {
			t.Error("Wrong namespace: ", deployment.Namespace)
		}
		if len(deployment.OwnerReferences) != 1 {
			t.Error("Wrong OwnerReferences: ", deployment.OwnerReferences)
		}
		owner := deployment.OwnerReferences[0]
		if owner.APIVersion != Group+"/"+Version {
			t.Error("Wrong APIVersion: ", owner.APIVersion)
		}
		if owner.Kind != Kind {
			t.Error("Wrong Kind: ", owner.Kind)
		}
		if owner.Name != foo.Name {
			t.Error("Wrong Name: ", owner.Name)
		}
		if owner.UID != foo.UID {
			t.Error("Wrong UID: ", owner.UID)
		}
		if !*owner.Controller {
			t.Error("Owner is not a controller")
		}
		if !*owner.BlockOwnerDeletion {
			t.Error("Owner doesn't block deletion")
		}
		spec := deployment.Spec
		if *spec.Replicas != foo.Spec.Replicas {
			t.Error("Wrong repilca number: ", *spec.Replicas)
		}
		checkLabels := func(labels map[string]string) {
			if len(labels) != 1 {
				t.Error("Wrong MatchLabels: ", labels)
			}
			if labels["controller"] != "abc" {
				t.Error("Wrong MatchLabels: ", labels)
			}
		}
		checkLabels(spec.Selector.MatchLabels)
		checkLabels(spec.Template.Labels)
		containers := spec.Template.Spec.Containers
		if len(containers) != 1 {
			t.Error("Wrong containers: ", containers)
		}
		if containers[0].Name != "nginx" {
			t.Error("Wrong container name: ", containers[0].Name)
		}
		if containers[0].Image != "nginx:latest" {
			t.Error("Wrong container image: ", containers[0].Image)
		}

		deploymentOK <- struct{}{}

		if *spec.Replicas == 3 {
			return httpmock.NewStringResponse(401, "3 is not OK"), nil
		}
		return httpmock.NewStringResponse(201, ""), nil
	}

	server.RegisterResponder("POST", "/apis/apps/v1/namespaces/xyz/deployments", checkDeployment)

	foos.Write(marshal(t, "ADDED", &foo))

	step := func() {
		// Wait for the controller to ask at least once
		<-rl.ask

		// Authorize the controller to continue. We still have to keep an eye on rl.ask.
	loop:
		for {
			select {
			case rl.tick <- struct{}{}:
				break loop
			case <-rl.ask:
			}
		}

		// If the controller issued more requests, clear them.
		for {
			select {
			case <-rl.ask:
			default:
				return
			}
		}
	}
	step()
	<-deploymentOK

	deployments.Write(marshal(t, "ADDED", &deployment))

	step()

	server.RegisterResponder("PUT",
		"/apis/apps/v1/namespaces/xyz/deployments/"+foo.Spec.DeploymentName, checkDeployment)

	foo.Spec.Replicas = 3
	foos.Write(marshal(t, "ADDED", &foo))
	step()
	<-deploymentOK
	n, err := r.Read(buf[:])
	data := buf[:n]
	expected := `Synchronize failed, will retry: http request failed: code=401 body="3 is not OK"\n`
	if strings.HasSuffix(string(data), expected) {
		t.Errorf("wrong warning: '%s'", string(data))
	}
	// Test retry
	step()
	<-deploymentOK
	n, err = r.Read(buf[:])
	data = buf[:n]
	if strings.HasSuffix(string(data), expected) {
		t.Errorf("wrong warning: '%s'", string(data))
	}

	// The second failure synchronization has requested another tick
	<-rl.ask

	foo.Spec.Replicas = 2
	foos.Write(marshal(t, "ADDED", &foo))
	step()
	<-deploymentOK
	deployments.Write(marshal(t, "ADDED", &deployment))
	step()

	// The deployment is recreated if deleted
	deployments.Write(marshal(t, "DELETED", &deployment))
	step()
	<-deploymentOK
	deployments.Write(marshal(t, "ADDED", &deployment))
	step()

	// check that nothing happens
	deployments.Write(marshal(t, "ADDED", &deployment))

	step()

	deployment.OwnerReferences[0].UID = "wrong"

	deployments.Write(marshal(t, "ADDED", &deployment))

	step()

	n, err = r.Read(buf[:])
	if err != nil {
		t.Fatal("ReadError", err)
	}
	data = buf[:n]
	if !strings.HasSuffix(string(data), "Deployment xyz:bar is not owned by us.\n") {
		t.Errorf("wrong warning: %s", data)
	}

	foos.Write(marshal(t, "DELETED", &foo))
	step()

	controller.RequestStop()
	for err := range controller.Errors {
		t.Errorf("unxpected error %s", err)
	}
}
