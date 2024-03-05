module github.com/1602/deploy-ga-k8s

go 1.22

// replace github.com/1602/witness => ../witness

require (
	github.com/1602/witness v0.0.0-20240304184741-bd137ec3a746
	github.com/google/go-github/v59 v59.0.0
	github.com/gosuri/uilive v0.0.4
	github.com/gosuri/uiprogress v0.0.1
	github.com/hako/durafmt v0.0.0-20210608085754-5c1018a4e16b
)

require (
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	golang.org/x/sys v0.17.0 // indirect
)
