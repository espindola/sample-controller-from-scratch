build:
	go build ./cmd/sample-controller

test:
	go test -v -count=1 ./pkg/controller -coverprofile cover.out -coverpkg ./pkg/controller,./pkg/kubeapi
