# Sample Controller

This is an alternative implementation of the [sample controller]
(https://github.com/kubernetes/sample-controller). The main
differences are

* It registers the CRD with the API server, so it is easier to use.
* It uses no generated code, making it easier to understand.

## Running

Start [minikube](https://minikube.sigs.k8s.io/docs/) or setup another
kubernetes environment and start the controller:

```sh
./sample-controller
```

The controller will register a CRD:

```sh
$ kubectl get crd
NAME                                CREATED AT
foos.samplecontroller.example.com   2020-10-30T20:47:37Z
```

Create a custom resource of that kind:

```sh
$ kubectl apply -f example-foo.yaml
```

Note that the controller creates the corresponding deployment

```sh
$ kubectl get deployments
NAME          READY   UP-TO-DATE   AVAILABLE   AGE
example-foo   0/1     1            0           3s
```

Modify the example to have 2 replicas and apply it again. The
controller will update the deployment

```sh
$ kubectl get deployments
NAME          READY   UP-TO-DATE   AVAILABLE   AGE
example-foo   1/2     2            1           90s
```
