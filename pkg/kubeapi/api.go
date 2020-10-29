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

type KubeClient struct {
	client http.Client
	url    url.URL
}

// host can be of the form https://192.168.39.239:8443
func NewClient(host string, transport http.RoundTripper) (*KubeClient, error) {
	u, err := url.Parse(host + apiPath + "/")
	if err != nil {
		return nil, err
	}
	return &KubeClient{client: http.Client{Transport: transport}, url: *u}, nil
}

type RequestError struct {
	StatusCode int
	Body       []byte
}

func (r *RequestError) Error() string {
	return fmt.Sprintf("http request failed: code=%d body=\"%s\"", r.StatusCode, r.Body)
}

// We take the namespace as an argument because some resources are not namespaced
func (client *KubeClient) do(method string, group string, version string, namespace string, path string, query url.Values,
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
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, &RequestError{StatusCode: resp.StatusCode, Body: body}
	}
	return resp, err
}

func (client *KubeClient) Get(group string, version string, namespace string, path string, query url.Values) (io.ReadCloser, error) {
	resp, err := client.do("GET", group, version, namespace, path, query, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (client *KubeClient) putOrPost(method string, group string, version string, namespace string, path string, obj interface{}) error {
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

func (client *KubeClient) Post(group string, version string, namespace string, path string, obj interface{}) error {
	return client.putOrPost("POST", group, version, namespace, path, obj)
}

func (client *KubeClient) Put(group string, version string, namespace string, path string, obj interface{}) error {
	return client.putOrPost("PUT", group, version, namespace, path, obj)
}

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

type WatchEvent struct {
	IsDelete bool
	Item     interface{}
	Err      error
}

func unmarshal(isDelete bool, data []byte, ty reflect.Type) WatchEvent {
	obj := reflect.New(ty)
	err := json.Unmarshal(data, obj.Interface())
	if err != nil {
		return WatchEvent{Err: fmt.Errorf("Unmarshaling of resource failed: %w", err)}
	}
	return WatchEvent{IsDelete: isDelete, Item: reflect.Indirect(obj).Interface()}
}

func (client *KubeClient) produceResources(group string, version string, namespace string, path string, query url.Values, v interface{},
	out chan<- WatchEvent, stopCh <-chan struct{}) {

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
		err := bodyReader.Close()
		if err != nil {
			out <- WatchEvent{Err: err}
		}
	}()

	decoder := json.NewDecoder(bodyReader)
	for {
		we := metav1.WatchEvent{}
		if err = decoder.Decode(&we); err != nil {
			// If we were asked to stop, don't report an error.
			select {
			case _ = <-stopCh:
				return
			default:
			}

			// Send the error, but still watch stopCh in case the client is not
			// interested.
			ev := WatchEvent{Err: fmt.Errorf("Could not decode WatchEvent(%s): %w", path,
				err)}
			select {
			case _ = <-stopCh:
				return
			case out <- ev:
			}
			return
		}
		et, err := parseEventType(we.Type)
		if err != nil {
			out <- WatchEvent{Err: err}
		}

		out <- unmarshal(et, we.Object.Raw, ty)
	}
}

func (client *KubeClient) GetResources(group string, version string, namespace string, path string, query url.Values, v interface{}) (<-chan WatchEvent,
	chan<- struct{}) {

	ch := make(chan WatchEvent)
	stop := make(chan struct{})
	go client.produceResources(group, version, namespace, path, query, v, ch, stop)
	return ch, stop
}

func (client *KubeClient) GetDeployments(namespace string) (<-chan WatchEvent, chan<- struct{}) {
	return client.GetResources("apps", "v1", namespace, "deployments", nil,
		appsv1.Deployment{})
}

func (client *KubeClient) AddDeployment(deployment *appsv1.Deployment) error {
	return client.Post("apps", "v1", deployment.Namespace, "deployments", deployment)
}

func (client *KubeClient) UpdateDeployment(deployment *appsv1.Deployment) error {
	return client.Put("apps", "v1", deployment.Namespace, "deployments/"+deployment.Name, deployment)
}

func (client *KubeClient) AddCustomResourceDefinition(crd *apiextensionsv1.CustomResourceDefinition) error {
	return client.Post("apiextensions.k8s.io", "v1", "", "customresourcedefinitions", crd)
}

func (client *KubeClient) GetCustomResourceDefinitions(name string) (<-chan WatchEvent,
	chan<- struct{}) {
	return client.GetResources("apiextensions.k8s.io", "v1", "", "customresourcedefinitions",
		url.Values{"fieldSelector": []string{"metadata.name=" + name}},
		apiextensionsv1.CustomResourceDefinition{})
}
