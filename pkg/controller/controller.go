package controller

import (
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"log"
	"sample-controller/pkg/kubeapi"
	"sample-controller/pkg/ratelimit"
)

const Version = "v1alpha1"
const Group = "samplecontroller.example.com"
const Kind = "Foo"

func addCRD(client *kubeapi.KubeClient, spec apiextensionsv1.CustomResourceDefinitionSpec) error {
	name := spec.Names.Plural + "." + spec.Group
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       spec,
	}

	err := client.AddCustomResourceDefinition(&crd)

	// Ignore 409 (Conflict)
	// FIXME: Update with a PUT with a metadata.resourceVersion.
	if re, ok := err.(*kubeapi.RequestError); ok && re.StatusCode != 409 {
		return re
	}

	resources, stop := client.GetCustomResourceDefinitions(name)
	defer close(stop)
Outer:
	for res := range resources {
		if res.Err != nil {
			return res.Err
		}
		if res.IsDelete {
			continue
		}
		item := res.Item.(apiextensionsv1.CustomResourceDefinition)
		for _, cond := range item.Status.Conditions {
			if cond.Type == "Established" &&
				cond.Status == apiextensionsv1.ConditionTrue {
				break Outer
			}
		}
	}
	return nil
}

func addFooCRD(client *kubeapi.KubeClient) error {
	crdNames := apiextensionsv1.CustomResourceDefinitionNames{
		Kind:   Kind,
		Plural: "foos",
	}
	crdSchemaSpec := apiextensionsv1.JSONSchemaProps{
		Type: "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{
			"deploymentName": apiextensionsv1.JSONSchemaProps{Type: "string"},
			"replicas":       apiextensionsv1.JSONSchemaProps{Type: "integer"},
		},
	}
	crdSchema := &apiextensionsv1.JSONSchemaProps{
		Type:       "object",
		Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": crdSchemaSpec},
	}
	crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
		Name:    Version,
		Schema:  &apiextensionsv1.CustomResourceValidation{OpenAPIV3Schema: crdSchema},
		Served:  true,
		Storage: true,
	}
	crdSpec := apiextensionsv1.CustomResourceDefinitionSpec{
		Group:    Group,
		Names:    crdNames,
		Scope:    "Namespaced",
		Versions: []apiextensionsv1.CustomResourceDefinitionVersion{crdVersion},
	}
	return addCRD(client, crdSpec)
}

type FooSpec struct {
	DeploymentName string `json:"deploymentName"`
	Replicas       int32  `json:"replicas"`
}

type Foo struct {
	metav1.ObjectMeta `json:"metadata"`
	Spec              FooSpec `json:"spec"`
}

type Controller struct {
	Namespace       string
	Errors          chan error
	stopFoos        chan<- struct{}
	stopDeployments chan<- struct{}

	rl ratelimit.RateLimiter

	client *kubeapi.KubeClient
}

// It is done once c.Errors is closed
func (c *Controller) RequestStop() {
	if c.stopFoos != nil {
		close(c.stopFoos)
	}
	if c.stopDeployments != nil {
		close(c.stopDeployments)
	}
}

type controllerStatus struct {
	// Map from name to spec
	foos map[string]Foo

	// Map from the name to deployment
	deployments map[string]appsv1.Deployment

	// Set of names of Foos we have to check
	todo map[string]struct{}
}

func newDeployment(foo *Foo) appsv1.Deployment {
	ref := metav1.NewControllerRef(foo, schema.GroupVersionKind{
		Group:   Group,
		Version: Version,
		Kind:    Kind,
	})
	meta := metav1.ObjectMeta{
		Name:            foo.Spec.DeploymentName,
		Namespace:       foo.Namespace,
		OwnerReferences: []metav1.OwnerReference{*ref},
	}
	labels := map[string]string{
		"controller": foo.Name,
	}
	container := corev1.Container{
		Name:  "nginx",
		Image: "nginx:latest",
	}
	template := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{container}},
	}
	spec := appsv1.DeploymentSpec{
		Selector: &metav1.LabelSelector{MatchLabels: labels},
		Template: template,
		Replicas: &foo.Spec.Replicas,
	}
	ret := appsv1.Deployment{
		ObjectMeta: meta,
		Spec:       spec,
	}
	return ret
}

func synchronize(client *kubeapi.KubeClient, status *controllerStatus) error {
	for item := range status.todo {
		// FIXME: Split a processsOneItem
		foo, has_foo := status.foos[item]
		if !has_foo {
			// There is nothing for us to do. The Kubernetes garbage collector will
			// delete the deployment for us.
			delete(status.todo, item)
			continue
		}

		dep, has_dep := status.deployments[foo.Spec.DeploymentName]
		if has_dep {
			if !metav1.IsControlledBy(&dep, &foo) {
				log.Printf("Deployment %s:%s is not owned by us.", dep.Namespace,
					dep.Name)
				// Don't delete from todo so we try again
				continue
			}
			if foo.Spec.Replicas == *dep.Spec.Replicas {
				delete(status.todo, item)
				continue
			}
		}

		newDep := newDeployment(&foo)
		var err error
		if has_dep {
			newDep.ResourceVersion = dep.ResourceVersion
			err = client.UpdateDeployment(&newDep)
		} else {
			err = client.AddDeployment(&newDep)
		}
		if err != nil {
			return err
		}
		delete(status.todo, item)

		// FIXME2: What happens if DeploymentName
		// changes? The original sample controller
		// just creates a new deployment, that is
		// almost certenly a bug.
	}
	return nil
}

// processResources goes over the existing Foos and Deployments
// and synchronizes them.
func processResources(c *Controller, deploymentsCh <-chan kubeapi.WatchEvent,
	foosCh <-chan kubeapi.WatchEvent) {
	defer close(c.Errors)

	status := controllerStatus{}
	status.foos = make(map[string]Foo)
	status.deployments = make(map[string]appsv1.Deployment)
	status.todo = make(map[string]struct{})

	addTODO := func(deployment *appsv1.Deployment) {
		// Only add to TODO if we own it
		for _, o := range deployment.OwnerReferences {
			// It is OK to not be supper strict in
			// here. We will just try to synchronize more
			// often.
			if o.Kind == Kind {
				c.rl.AskTick()
				status.todo[o.Name] = struct{}{}
				return
			}
		}
	}

	for {
		select {
		case d, ok := <-deploymentsCh:
			if d.Err != nil {
				c.Errors <- fmt.Errorf("Reading deployments: %w", d.Err)
				return
			}
			if !ok {
				deploymentsCh = nil
				break
			}
			newDeployment := d.Item.(appsv1.Deployment)
			oldDeployment, ok := status.deployments[newDeployment.Name]
			if d.IsDelete {
				delete(status.deployments, newDeployment.Name)
			} else {
				status.deployments[newDeployment.Name] = newDeployment
			}

			addTODO(&newDeployment)
			if ok {
				addTODO(&oldDeployment)
			}

		case f, ok := <-foosCh:
			if f.Err != nil {
				c.Errors <- fmt.Errorf("Reading Foos: %w", f.Err)
				return
			}
			if !ok {
				foosCh = nil
				break
			}
			newFoo := f.Item.(Foo)
			c.rl.AskTick()
			if f.IsDelete {
				delete(status.foos, newFoo.Name)
			} else {
				status.foos[newFoo.Name] = newFoo
			}
			status.todo[newFoo.Name] = struct{}{}

		case <-c.rl.GetChan():
			if err := synchronize(c.client, &status); err != nil {
				log.Printf("Synchronize failed, will retry: %s", err)
				c.rl.AskTick()
			}
		}

		// We are done if both channels were closed
		if deploymentsCh == nil && foosCh == nil {
			return
		}
	}
}

func NewController(client *kubeapi.KubeClient, rl ratelimit.RateLimiter,
	namespace string) *Controller {
	ret := &Controller{}

	errors := make(chan error)
	ret.Errors = errors

	ret.rl = rl
	ret.client = client
	ret.Namespace = namespace

	ret.start()

	return ret
}

func (c *Controller) startAux() {
	err := addFooCRD(c.client)
	if err != nil {
		c.Errors <- fmt.Errorf("Could not add CRD: %w", err)
		close(c.Errors)
		return
	}

	foosCh, stopFoos := c.client.GetResources(Group, Version, c.Namespace, "foos", nil, Foo{})
	c.stopFoos = stopFoos

	deploymentsCh, stopDeployments := c.client.GetDeployments(c.Namespace)
	c.stopDeployments = stopDeployments

	processResources(c, deploymentsCh, foosCh)
}

func (c *Controller) start() {
	go c.startAux()
}
