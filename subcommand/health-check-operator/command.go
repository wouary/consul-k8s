package healthcheckoperator

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"

	hcko "github.com/hashicorp/consul-k8s/operators/health-check"
	"github.com/hashicorp/consul-k8s/subcommand"
	"github.com/hashicorp/consul-k8s/subcommand/flags"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
)

const (
	numRetries = 10
)

// Command is the command for running the Health Checks Kubernetes Operator
// which syncs health probe status of Pods to Consul for service mesh traffic flow
// optimizations.
type Command struct {
	UI cli.Ui

	flags                  *flag.FlagSet
	http                   *flags.HTTPFlags
	k8s                    *flags.K8SFlags
	flagListen             string
	flagK8SSourceNamespace string
	flagLogLevel           string
	flagAgentPort          string

	// Flags to support namespaces
	flagEnableNamespaces bool // Use namespacing on all components

	consulClient *api.Client
	clientset    kubernetes.Interface

	once   sync.Once
	sigCh  chan os.Signal
	help   string
	logger hclog.Logger
}

func (c *Command) init() {
	c.flags = flag.NewFlagSet("", flag.ContinueOnError)
	c.flags.StringVar(&c.flagListen, "listen", ":8080", "Address to bind listener to.")
	// TODO: do not think this is needed
	c.flags.StringVar(&c.flagK8SSourceNamespace, "k8s-source-namespace", metav1.NamespaceAll,
		"The Kubernetes namespace to watch for service changes and sync to Consul. "+
			"If this is not set then it will default to all namespaces.")
	c.flags.StringVar(&c.flagLogLevel, "log-level", "info",
		"Log verbosity level. Supported values (in order of detail) are \"trace\", "+
			"\"debug\", \"info\", \"warn\", and \"error\".")
	c.flags.StringVar(&c.flagAgentPort, "agent-port", "8500", "The server agent port to use when connecting, 8500/8501")

	c.http = &flags.HTTPFlags{}
	c.k8s = &flags.K8SFlags{}
	flags.Merge(c.flags, c.http.Flags())
	flags.Merge(c.flags, c.k8s.Flags())

	c.help = flags.Usage(help, c.flags)

	// Wait on an interrupt to exit. This channel must be initialized before
	// Run() is called so that there are no race conditions where the channel
	// is not defined.
	if c.sigCh == nil {
		c.sigCh = make(chan os.Signal, 1)
		signal.Notify(c.sigCh, os.Interrupt)
	}
}

func (c *Command) Run(args []string) int {
	c.once.Do(c.init)
	if err := c.flags.Parse(args); err != nil {
		c.UI.Info(fmt.Sprintf("%v", err))
		return 1
	}
	if len(c.flags.Args()) > 0 {
		c.UI.Error(fmt.Sprintf("Should have no non-flag arguments."))
		return 1
	}

	// Create the k8s clientset
	if c.clientset == nil {
		config, err := subcommand.K8SConfig(c.k8s.KubeConfig())
		if err != nil {
			c.UI.Error(fmt.Sprintf("Error retrieving Kubernetes auth: %s", err))
			return 1
		}
		c.clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			c.UI.Error(fmt.Sprintf("Error initializing Kubernetes client: %s", err))
			return 1
		}
	}

	// Set up logging
	if c.logger == nil {
		level := hclog.LevelFromString(c.flagLogLevel)
		if level == hclog.NoLevel {
			c.UI.Error(fmt.Sprintf("Unknown log level: %s", c.flagLogLevel))
			return 1
		}
		c.logger = hclog.New(&hclog.LoggerOptions{
			Level:  level,
			Output: os.Stderr,
		})
	}

	// Create the context we'll use to cancel everything
	ctx, cancelF := context.WithCancel(context.Background())

	// Start Consul-to-K8S sync
	var healthCh chan struct{}
	// construct the Controller object which has all of the necessary components to
	// handle logging, connections, informing (listing and watching), the queue,
	// and the handler
	healthCheckHandler := &hcko.HealthCheckHandler{
		Log:                c.logger.Named("healthcheckHandler"),
		Flags:              c.flags,
		HFlags:             c.http,
		ConsulClientScheme: runtime.NewScheme().Name(),
		Client:             c.consulClient,
		Clientset:          c.clientset,
	}

	// Build the controller and start it
	cont := &hcko.Controller{
		Log:        c.logger.Named("healthcheckController"),
		Clientset:  c.clientset,
		Informer:   nil,
		Queue:      nil,
		Handle:     healthCheckHandler,
		MaxRetries: numRetries,
		Namespace:  c.flagK8SSourceNamespace,
	}

	// Start healthcheck health handler
	// TODO: currently a no-op because consulClient is not initiated yet
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health/ready", c.handleReady)
		var handler http.Handler = mux

		c.UI.Info(fmt.Sprintf("Listening on %q...", c.flagListen))
		if err := http.ListenAndServe(c.flagListen, handler); err != nil {
			c.UI.Error(fmt.Sprintf("Error listening: %s", err))
		}
	}()

	// Start the HealthCheck controller
	healthCh = make(chan struct{})
	go func() {
		defer close(healthCh)
		cont.Run(ctx.Done())
	}()

	select {
	// Unexpected exit
	case <-healthCh:
		cancelF()
		return 1

	// Interrupted, gracefully exit
	case <-c.sigCh:
		cancelF()
		if healthCh != nil {
			<-healthCh
		}
		return 0
	}
}

func (c *Command) Synopsis() string { return synopsis }
func (c *Command) Help() string {
	c.once.Do(c.init)
	return c.help
}

// interrupt sends os.Interrupt signal to the command
// so it can exit gracefully. This function is needed for tests
func (c *Command) interrupt() {
	c.sigCh <- os.Interrupt
}

// TODO: fix
const synopsis = "Sync Kubernetes Health Check transitions with Consul services."
const help = `
Usage: consul-k8s health-checks [options]

  Syncs Kubernetes Health Check Pod transitions with Consul.
  When a k8s probe fails and the Pod is marked Unhealthy the
  transition will be sent down to Consul in form of a Consul
  TTL health check which allows Consul to direct traffic
  accordingly.

`

func (c *Command) handleReady(rw http.ResponseWriter, req *http.Request) {
	// The main readiness check is whether sync can talk to
	// the consul cluster, in this case querying for the leader
	// TODO: consulClient wont be valid here bc we instantiate it at runtime..
	// Do we need a second consulClient?
	/*_, err := c.consulClient.Status().Leader()
	if err != nil {
		c.UI.Error(fmt.Sprintf("[GET /health/ready] Error getting leader status: %s", err))
		rw.WriteHeader(500)
		return
	}
	*/
	rw.WriteHeader(204)
}
