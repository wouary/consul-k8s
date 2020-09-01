package healthcheckoperator

import (
	"context"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/consul/sdk/testutil"
	"github.com/hashicorp/consul/sdk/testutil/retry"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	"github.com/stretchr/testify/require"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

// Test that when -add-k8s-namespace-suffix flag is used
// k8s namespaces are appended to the service names synced to Consul
func TestRun_WithK8SSourceNamespace(t *testing.T) {
	t.Parallel()

	k8s, testServer := completeSetup(t)
	defer testServer.Stop()

	consulClient, err := api.NewClient(&api.Config{
		Address: testServer.HTTPAddr,
	})
	require.NoError(t, err)

	// Run the command.
	ui := cli.NewMockUi()
	cmd := Command{
		UI:           ui,
		clientset:    k8s,
		consulClient: consulClient,
		logger: hclog.New(&hclog.LoggerOptions{
			Name:  t.Name(),
			Level: hclog.Debug,
		}),
		flagK8SSourceNamespace: "test",
	}

	// create a service in k8s
	_, err = k8s.CoreV1().Services(cmd.flagK8SSourceNamespace).Create(context.Background(), lbService("foo", "1.1.1.1"), metav1.CreateOptions{})
	require.NoError(t, err)

	exitChan := runCommandAsynchronously(&cmd, []string{
		// change the write interval, so we can see changes in Consul quicker
		"-consul-write-interval", "100ms",
		"-add-k8s-namespace-suffix",
	})
	defer stopCommand(t, &cmd, exitChan)

	retry.Run(t, func(r *retry.R) {
		services, _, err := consulClient.Catalog().Services(nil)
		require.NoError(r, err)
		require.Len(r, services, 2)
		require.Contains(r, services, "foo-default")
	})
}

// Set up test consul agent and fake kubernetes cluster client
func completeSetup(t *testing.T) (*fake.Clientset, *testutil.TestServer) {
	k8s := fake.NewSimpleClientset()

	svr, err := testutil.NewTestServerConfigT(t, nil)
	require.NoError(t, err)

	return k8s, svr
}

// This function starts the command asynchronously and returns a non-blocking chan.
// When finished, the command will send its exit code to the channel.
// Note that it's the responsibility of the caller to terminate the command by calling stopCommand,
// otherwise it can run forever.
func runCommandAsynchronously(cmd *Command, args []string) chan int {
	// We have to run cmd.init() to ensure that the channel the command is
	// using to watch for os interrupts is initialized. If we don't do this,
	// then if stopCommand is called immediately, it will block forever
	// because it calls interrupt() which will attempt to send on a nil channel.
	cmd.init()
	exitChan := make(chan int, 1)

	go func() {
		exitChan <- cmd.Run(args)
	}()

	return exitChan
}

func stopCommand(t *testing.T, cmd *Command, exitChan chan int) {
	if len(exitChan) == 0 {
		cmd.interrupt()
	}
	select {
	case c := <-exitChan:
		require.Equal(t, 0, c, string(cmd.UI.(*cli.MockUi).ErrorWriter.Bytes()))
	}
}

// lbService returns a Kubernetes service of type LoadBalancer.
func lbService(name, lbIP string) *apiv1.Service {
	return &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{},
		},

		Spec: apiv1.ServiceSpec{
			Type: apiv1.ServiceTypeLoadBalancer,
		},

		Status: apiv1.ServiceStatus{
			LoadBalancer: apiv1.LoadBalancerStatus{
				Ingress: []apiv1.LoadBalancerIngress{
					{
						IP: lbIP,
					},
				},
			},
		},
	}
}
