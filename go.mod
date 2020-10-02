module github.com/espindola/sample-controller

go 1.15

require (
	github.com/jarcoal/httpmock v1.0.6
	k8s.io/api v0.19.2
	k8s.io/apiextensions-apiserver v0.19.2
	k8s.io/apimachinery v0.19.2
	k8s.io/client-go v0.19.2
	sample-controller v0.0.0
)

replace (
	k8s.io/client-go => /home/espindola/scylla/client-go
	sample-controller => ./
)
