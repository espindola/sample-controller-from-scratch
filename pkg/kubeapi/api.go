package kubeapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"net/http"
	"net/url"
	"reflect"
)

const apiPath = "/apis"

// KubeClient represents a client to a kubernetes API server.
type KubeClient struct {
	client http.Client
	url    url.URL
}

// NewClient returns a new KubeClient. The host is a string encoding
// the url of the api server (https://192.168.39.239:8443 for
// expample).
func NewClient(host string, transport http.RoundTripper) (*KubeClient, error) {
	u, err := url.Parse(host + apiPath + "/")
	if err != nil {
		return nil, err
	}
	return &KubeClient{client: http.Client{Transport: transport}, url: *u}, nil
}

// RequestError represents an http reply with an unsuccessful status code.
type RequestError struct {
	StatusCode int
	Body       []byte
}

func (r *RequestError) Error() string {
	return fmt.Sprintf("http request failed: code=%d body=\"%s\"", r.StatusCode, r.Body)
}

func (client *KubeClient) do(method, group, version, namespace, path string, query url.Values,
	data []byte) (*http.Response, error) {
	url := client.url
	url.Path += group + "/"
	url.Path += version + "/"
	if namespace != "" {
		url.Path += "namespaces/" + namespace + "/"
	}
	url.Path += path
	url.RawQuery = query.Encode()
	reader := ioutil.NopCloser(bytes.NewReader(data))
	req := http.Request{Method: method, URL: &url, Body: reader}
	resp, err := client.client.Do(&req)
	if err == nil && !(resp.StatusCode >= 200 && resp.StatusCode < 300) {
		defer resp.Body.Close()
		// Ignore any errors from ReadAll, they are probably not as interesting as the
		// RequestError
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, &RequestError{StatusCode: resp.StatusCode, Body: body}
	}
	return resp, err
}

// Get does a GET request on a resource. Group is the Kubernetes API
// group (apiextensions.k8s.io for example). An empty namespace means
// this is accessing a non namespaced resource (not the default
// namespace). An unsuccessful response is converted to an error, so
// this just returns a io.ReadCloser for the body.
func (client *KubeClient) Get(group, version, namespace, path string,
	query url.Values) (io.ReadCloser, error) {
	resp, err := client.do("GET", group, version, namespace, path, query, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (client *KubeClient) putOrPost(method, group, version, namespace, path string,
	obj interface{}) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}

	resp, err := client.do(method, group, version, namespace, path, nil, data)
	if err == nil {
		err = resp.Body.Close()
	}
	return err
}

// Post does a POST request on a resource. The body is the json
// marshaling of obj. See Get for other parameters.
func (client *KubeClient) Post(group, version, namespace, path string, obj interface{}) error {
	return client.putOrPost("POST", group, version, namespace, path, obj)
}

// Put does a PUT request on a resource. See Post for the parameters.
func (client *KubeClient) Put(group, version, namespace, path string, obj interface{}) error {
	return client.putOrPost("PUT", group, version, namespace, path, obj)
}

// Delete does a DELETE request on a resource. See Post for the parameters.
func (client *KubeClient) Delete(group, version, namespace, path string) error {
	resp, err := client.do("DELETE", group, version, namespace, path, nil, nil)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

// Returns true if the event is DELETED
func parseEventType(ty string) (bool, error) {
	// We get a full copy with MODIFIED, so we can treat it as ADDED
	switch ty {
	case "ADDED", "MODIFIED":
		return false, nil
	case "DELETED":
		return true, nil
	}
	return false, fmt.Errorf("Invalid EventType: %s", ty)
}

// WatchEvent is a simplified view of
// k8s.io/apimachinery/pkg/apis/meta/v1.WatchEvent. Unlike the
// original, we only differentiate delete/add and the object is
// decoded instead of a raw json string. Any error obtaining or
// parsing this event is reported in Err.
type WatchEvent struct {
	IsDelete bool
	Item     interface{}
	Err      error
}

func (client *KubeClient) produceResources(group, version, namespace, path string,
	query url.Values, v interface{}, out chan<- WatchEvent, stopCh <-chan struct{}) {
	defer close(out)
	ty := reflect.TypeOf(v)
	if query == nil {
		query = url.Values{}
	}
	query["watch"] = []string{"true"}

	bodyReader, err := client.Get(group, version, namespace, path, query)
	if err != nil {
		out <- WatchEvent{Err: fmt.Errorf("Watch failed: %w", err)}
		return
	}

	go func() {
		_ = <-stopCh
		// Closing bodyReader is probably the only way to stop
		// decoder.Decode bellow.
		err := bodyReader.Close()
		// The call to Close should not fail. If it does,
		// there is nothing for us to do but panic. We cannot
		// send the error to the out channel as it might be
		// closed. It should also not be ignored, as the loop
		// bellow might be forever stuck in Decode.
		if err != nil {
			panic(err)
		}
	}()

	send := func(ev WatchEvent) {
		// If we were asked to stop, don't send. The event
		// might be the last error produced by closing
		// bodyReader.
		select {
		case _ = <-stopCh:
			return
		default:
		}

		// Send, but still watch stopCh in case the client is
		// not interested.
		select {
		case _ = <-stopCh:
			return
		case out <- ev:
		}
	}

	decoder := json.NewDecoder(bodyReader)
	for {
		we := metav1.WatchEvent{}
		if err = decoder.Decode(&we); err != nil {
			err = fmt.Errorf("Could not decode WatchEvent(%s): %w", path, err)
			send(WatchEvent{Err: err})
			return
		}
		isDelete, err := parseEventType(we.Type)
		if err != nil {
			send(WatchEvent{Err: err})
			return
		}

		obj := reflect.New(ty)
		err = json.Unmarshal(we.Object.Raw, obj.Interface())
		if err != nil {
			err = fmt.Errorf("Unmarshaling of resource failed: %w", err)
			send(WatchEvent{Err: err})
			return
		}
		send(WatchEvent{IsDelete: isDelete, Item: reflect.Indirect(obj).Interface()})
	}
}

// GetResources queries the api server for a particular resource and
// sends the resulting WatchEvent to a returned channel. It also
// returns a second channel that should be closed to request
// GetResources to stop. The type of the resource is identified by
// v. The produced WatchEvents will have Items of the same type as v.
func (client *KubeClient) GetResources(group, version, namespace, path string, query url.Values,
	v interface{}) (<-chan WatchEvent, chan<- struct{}) {
	ch := make(chan WatchEvent)
	stop := make(chan struct{})
	go client.produceResources(group, version, namespace, path, query, v, ch, stop)
	return ch, stop
}

// GetDeployments queries the api server for deployments. See GetResources for details.
func (client *KubeClient) GetDeployments(namespace string) (<-chan WatchEvent, chan<- struct{}) {
	return client.GetResources("apps", "v1", namespace, "deployments", nil,
		appsv1.Deployment{})
}

// AddDeployment adds a new deployment.
func (client *KubeClient) AddDeployment(deployment *appsv1.Deployment) error {
	return client.Post("apps", "v1", deployment.Namespace, "deployments", deployment)
}

// UpdateDeployment replaces an existing deployment.
func (client *KubeClient) UpdateDeployment(deployment *appsv1.Deployment) error {
	return client.Put("apps", "v1", deployment.Namespace, "deployments/"+deployment.Name,
		deployment)
}

// DeleteDeployment deletes a deployment.
func (client *KubeClient) DeleteDeployment(deployment *appsv1.Deployment) error {
	return client.Delete("apps", "v1", deployment.Namespace, "deployments/"+deployment.Name)
}

// AddCustomResourceDefinition adds a new CRD.
func (client *KubeClient) AddCustomResourceDefinition(crd *apiextensionsv1.CustomResourceDefinition) error {
	return client.Post("apiextensions.k8s.io", "v1", "", "customresourcedefinitions", crd)
}

// GetCustomResourceDefinitions queries the api server for CRDs. See GetResources for details.
func (client *KubeClient) GetCustomResourceDefinitions(name string) (<-chan WatchEvent,
	chan<- struct{}) {
	return client.GetResources("apiextensions.k8s.io", "v1", "", "customresourcedefinitions",
		url.Values{"fieldSelector": []string{"metadata.name=" + name}},
		apiextensionsv1.CustomResourceDefinition{})
}
