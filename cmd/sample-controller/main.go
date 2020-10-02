package main

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"os/user"
	"sample-controller/pkg/controller"
	"sample-controller/pkg/kubeapi"
	"sample-controller/pkg/ratelimit"
)

func main() {
	usr, err := user.Current()
	if err != nil {
		panic(err)
	}
	config, err := clientcmd.BuildConfigFromFlags("", usr.HomeDir+"/.kube/config")
	if err != nil {
		panic(err)
	}

	transport, err := rest.TransportFor(config)
	if err != nil {
		panic(err)
	}

	client, err := kubeapi.NewClient(config.Host, transport)
	if err != nil {
		panic(err)
	}

	controller := controller.NewController(client, ratelimit.AfterOneSecondIdle(), "default")

	done := make(chan struct{})
	go func() {
		for err := range controller.Errors {
			panic(err)
		}
		close(done)
	}()

	var v [1]byte
	os.Stdin.Read(v[:])
	controller.RequestStop()
	<-done
}
