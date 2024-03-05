package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/1602/witness"
	"github.com/google/go-github/v59/github"
	"github.com/gosuri/uilive"
	"github.com/gosuri/uiprogress"
	"github.com/gosuri/uiprogress/util/strutil"
	"github.com/hako/durafmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/homedir"
)

func example() {
	writer := uilive.New()

	// start listening for updates and render
	writer.Start()

	for _, f := range [][]string{{"Foo.zip", "Bar.iso"}, {"Baz.tar.gz", "Qux.img"}} {
		for i := 0; i <= 50; i++ {
			_, _ = fmt.Fprintf(writer, "Downloading %s.. (%d/%d) GB\n", f[0], i, 50)
			_, _ = fmt.Fprintf(writer.Newline(), "Downloading %s.. (%d/%d) GB\n", f[1], i, 50)
			time.Sleep(time.Millisecond * 25)
		}
		_, _ = fmt.Fprintf(writer.Bypass(), "Downloaded %s\n", f[0])
		_, _ = fmt.Fprintf(writer.Bypass(), "Downloaded %s\n", f[1])
	}
	_, _ = fmt.Fprintln(writer, "Finished: Downloaded 150GB")
	writer.Stop() // flush and stop rendering
}

var writer *uilive.Writer
var owner string
var repo string
var workflow string
var help bool
var debug bool

func main() {
	flag.StringVar(&workflow, "workflow", "cd", "Github workflow name")
	flag.StringVar(&repo, "repo", "", "Repository name")
	flag.StringVar(&owner, "owner", "ubio", "Repository owner")
	flag.BoolVar(&help, "h", false, "Display command line flags")
	flag.BoolVar(&debug, "debug", false, "Debug http requests with witness")

	flag.Parse()

	if repo == "" {
		cmd := exec.Command("git", "config", "--get", "remote.origin.url")
		o, err := cmd.Output()
		if err == nil {
			t := strings.Split(strings.TrimSuffix(strings.TrimSpace(strings.Split(string(o), ":")[1]), ".git"), "/")
			if len(t) == 2 {
				owner = t[0]
				repo = t[1]
			}
		}
	}

	if help {
		flag.PrintDefaults()
		return
	}

	if repo == "" {
		fmt.Println("Specify repository name, for example service-api")
		return
	}

	cl := &http.Client{}
	if debug {
		witness.DebugClient(cl, context.TODO())
	}

	workflow = strings.ToLower(workflow)

	writer = uilive.New()
	writer.Start()
	gh(cl)
	// k8s()
	writer.Stop()
}

type ActiveRunDetails struct {
	total    int
	current  int
	stepName string
	isDone   bool
	status   string
}

func gh(cl *http.Client) {

	client := github.NewClient(cl).WithAuthToken(os.Getenv("GH_ACCESS_TOKEN"))

	ctx := context.TODO()

	g := &GitHubAdapter{
		owner:  owner,
		repo:   repo,
		client: client,
		writer: writer,
	}

	g.Print("searching workflows")

	ww, _, err := client.Actions.ListWorkflows(ctx, owner, repo, &github.ListOptions{})
	if err != nil {
		log.Fatal(err)
	}

	var id int64

	for _, w := range ww.Workflows {
		if strings.ToLower(*w.Name) == workflow {
			id = *w.ID
			g.Print("found '%s', checking latest runs", workflow)
		}
	}

	if id == 0 {
		fmt.Printf("Unable to find workflow matching '%s'\nFound these workflows:\n", workflow)
		for _, w := range ww.Workflows {
			duration, _ := durafmt.ParseString(fmt.Sprint(time.Since(w.UpdatedAt.Time).Round(time.Hour)))
			fmt.Printf(" - %s, %s \n", *w.Name, duration)
		}
		return
	}

	run, err := g.GetLatestWorkflowRun(ctx, id)
	if err != nil {
		log.Fatal(err)
	}

	var icon = "✅"
	if run.Conclusion == nil {
		uiprogress.Start()

		var bar *uiprogress.Bar

		ard := &ActiveRunDetails{total: 1}

		for {
			err = g.getActiveRunDetails(ctx, *run.ID, ard)
			if ard.status == "queued" {
				g.Print("job is queued")
				time.Sleep(time.Second)
				continue
			}

			if bar == nil {
				g.Print("running job")
				bar = uiprogress.AddBar(ard.total).AppendCompleted()
				bar.PrependFunc(func(b *uiprogress.Bar) string {
					return strutil.PrettyTime(time.Since(run.RunStartedAt.Time))
				})
				bar.PrependFunc(func(b *uiprogress.Bar) string {
					return strutil.Resize(fmt.Sprintf("%d/%d. %s ", ard.current, ard.total, ard.stepName), 30)
				})
				bar.Set(0)
			}

			bar.Total = ard.total

			if err != nil {
				log.Fatal(err)
			}

			bar.Set(ard.current)

			if ard.isDone {
				break
			}

			time.Sleep(time.Second)
		}
		uiprogress.Stop()

		for {
			run, err = g.GetLatestWorkflowRun(ctx, id)
			if run.Conclusion != nil {
				break
			}
		}
	}

	if *run.Conclusion != "success" {
		icon = "💥"
	}

	fmt.Fprintf(writer, "%s %s/%s workflows: run '%s' by %s %s as %s at %s\n", icon, owner, repo, *run.DisplayTitle, *run.Actor.Login, *run.Status, *run.Conclusion, *run.CreatedAt)

	time.Sleep(time.Second * 1)

	if workflow == "cd" {
		prs, _, err := g.client.PullRequests.List(ctx, owner, "infrastructure", &github.PullRequestListOptions{
			State:     "open",
			Sort:      "created",
			Direction: "desc",
			ListOptions: github.ListOptions{
				Page:    0,
				PerPage: 10,
			},
		})

		if err != nil {
			log.Fatal(err)
		}

		var foundPR *github.PullRequest

		for _, pr := range prs {
			if strings.Contains(*pr.Title, g.repo) && strings.Contains(*pr.Title, *run.DisplayTitle) && strings.Contains(*pr.Title, "production") {
				foundPR = pr
			}
		}

		if foundPR != nil {
			for i := range 5 {
				g.Print("Found open infra PR: %s, merging in %d...", *foundPR.Title, 5-i)
				time.Sleep(time.Second)
			}
		}
	}
}

type GitHubAdapter struct {
	owner  string
	repo   string
	client *github.Client
	writer *uilive.Writer
}

func (g *GitHubAdapter) getActiveRunDetails(ctx context.Context, id int64, ard *ActiveRunDetails) (err error) {
	jobs, _, err := g.client.Actions.ListWorkflowJobs(ctx, g.owner, g.repo, id, &github.ListWorkflowJobsOptions{
		Filter: "latest",
		ListOptions: github.ListOptions{
			Page:    0,
			PerPage: 1,
		},
	})

	if err != nil {
		return
	}

	if *jobs.TotalCount == 0 {
		return
	}

	ard.status = *jobs.Jobs[0].Status

	steps := jobs.Jobs[0].Steps
	ard.total = len(steps)
	ard.current = 0

	if jobs.Jobs[0].Conclusion != nil {
		ard.isDone = true
		return
	}

	for i, step := range steps {
		if step.Conclusion == nil {
			ard.current = i + 1
			ard.stepName = *step.Name
			return
		}
	}

	return
}

func (g *GitHubAdapter) GetLatestWorkflowRun(ctx context.Context, workflowId int64) (*github.WorkflowRun, error) {
	wfr, _, err := g.client.Actions.ListWorkflowRunsByID(ctx, g.owner, g.repo, workflowId, &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{
			Page:    0,
			PerPage: 1,
		},
	})

	if *wfr.TotalCount == 0 {
		log.Fatal("No workflow runs found")
	}

	if err != nil {
		return nil, err
	}

	return wfr.WorkflowRuns[0], nil
}

func (g *GitHubAdapter) Print(f string, a ...any) {
	_, _ = fmt.Fprintf(g.writer, "Workflow '%s' run for %s/%s: "+f+"\n", append([]any{workflow, g.owner, g.repo}, a...)...)
}

func k8s() {
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	config.AuthProvider = nil
	config.ExecProvider = &api.ExecConfig{
		Command:            "gke-gcloud-auth-plugin",
		APIVersion:         "client.authentication.k8s.io/v1beta1",
		InstallHint:        "Requires gke-gcloud-auth-plugin",
		ProvideClusterInfo: true,
		InteractiveMode:    api.IfAvailableExecInteractiveMode,
	}

	cl, err := rest.HTTPClientFor(config)
	// witness.DebugClient(cl, context.TODO())

	// create the clientset
	clientset, err := kubernetes.NewForConfigAndClient(config, cl)
	if err != nil {
		panic(err.Error())
	}

	for {
		pods, err := clientset.CoreV1().Pods("kube-system").List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}
		fmt.Printf("There are %d pods in the cluster\n", len(pods.Items))

		// Examples for error handling:
		// - Use helper functions like e.g. errors.IsNotFound()
		// - And/or cast to StatusError and use its properties like e.g. ErrStatus.Message
		namespace := "kube-system"
		pods, err = clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{LabelSelector: "k8s-app=kube-dns"})

		time.Sleep(1 * time.Second)
	}
}
