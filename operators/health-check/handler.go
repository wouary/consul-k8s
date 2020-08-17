package healthcheckoperator

// https://github.com/trstringer/k8s-controller-core-resource

import (
	"flag"
	"fmt"
	"github.com/cenkalti/backoff"
	"github.com/hashicorp/consul-k8s/subcommand/flags"
	"github.com/hashicorp/consul/api"
	log "github.com/hashicorp/go-hclog"
	core_v1 "k8s.io/api/core/v1"
	"time"
)

// Handler interface contains the methods that are required
type Handler interface {
	Init() error
	ObjectCreated(obj interface{}) error
	ObjectDeleted(obj interface{}) error
	ObjectUpdated(objOld, objNew interface{}) error
}

// HealthCheckHandler is a sample implementation of Handler
type HealthCheckHandler struct {
	Log                log.Logger
	AclConfig          api.NamespaceACLConfig
	Client             *api.Client
	Flags              *flag.FlagSet
	HFlags             *flags.HTTPFlags
	ConsulClientScheme string
}

// getConsulHealthCheckID deterministically generates a health check ID that will be unique to the Agent
// where the health check is registered and deregistered.
func (t *HealthCheckHandler) getConsulHealthCheckID(pod *core_v1.Pod) string {
	return t.getConsulServiceID(pod) + "-health-check-failing-ttl"
}

// return the serviceID of the connect service
func (t *HealthCheckHandler) getConsulServiceID(pod *core_v1.Pod) string {
	return pod.Name + "-" + pod.Annotations["consul.hashicorp.com/connect-service"]
}

// registerConsulHealthCheck registers a Failing TTL health check for the service on this Agent.
// The Agent is local to the Pod which has failed a health check.
// This has the effect of marking the endpoint unhealthy for Consul mesh traffic.
func (t *HealthCheckHandler) registerConsulHealthCheck(consulHealthCheckID, serviceID string) error {
	t.Log.Info("registerHealthCheck, %v %v", consulHealthCheckID, serviceID)
	// There is a chance of a race between when the Pod is transitioned to healthy by k8s and when we've initially
	// completed the registration of the service with the Consul Agent on this node. Retry a few times to be sure
	// that the service does in fact exist, otherwise it will return 500 from Consul API.
	retries := 0
	var err error
	err = backoff.Retry(func() error {
		if retries > 10 {
			err = fmt.Errorf("did not find serviceID: %v", serviceID)
			return nil
		}
		retries++
		svc, err := t.Client.Agent().Services()
		if err == nil {
			for _, y := range svc {
				if y.Service == serviceID {
					return nil
				}
			}
			return fmt.Errorf("did not find serviceID: %v", serviceID)
		}
		return err
	}, backoff.NewConstantBackOff(1*time.Second))
	if err != nil {
		return err
	}
	err = t.Client.Agent().CheckRegister(&api.AgentCheckRegistration{
		Name:      consulHealthCheckID,
		Notes:     "Failing TTL health check for pod status update",
		ServiceID: serviceID,
		AgentServiceCheck: api.AgentServiceCheck{
			//Name:                           "TestCheck",
			TTL:                            "100h",
			Status:                         "critical",
			Notes:                          "",
			TLSSkipVerify:                  true,
			SuccessBeforePassing:           1,
			FailuresBeforeCritical:         1,
			DeregisterCriticalServiceAfter: "",
		},
		Namespace: "",
	})
	if err != nil {
		t.Log.Error("Unable to register failing ttl health check, %v", err)
		return err
	}
	return nil
}

// deregisterConsulHealthCheck deregisters a Failing TTL health check for the service on this Agent.
// The Agent is local to the Pod which has a succeeding health check.
// This has the effect of marking the endpoint healthy for Consul mesh traffic.
func (t *HealthCheckHandler) deregisterConsulHealthCheck(consulHealthCheckID string) error {
	// TODO: figure out how to handle cases where the health check doesnt exist due to sync issues
	t.Log.Info("deregisterConsulHealthCheck for %v", consulHealthCheckID)
	err := t.Client.Agent().CheckDeregister(consulHealthCheckID)
	if err != nil {
		t.Log.Error("Unable to deregister failing ttl health check, %v", err)
		return err
	}
	return nil
}

// updateConsulClient creates a new connection to Consul pointed at the hostIP
// of the Pod in order to make Agent calls for health check registration/deregistration.
func (t *HealthCheckHandler) updateConsulClient(pod *core_v1.Pod) error {
	hostIP := pod.Status.HostIP
	var err error
	// TODO: This is pretty hacky
	// TODO: submit PR for HTTPFlags.SetAddress API
	// TODO:  figure out how to pass agent-port :  t.Flags.Lookup("agent-port").Name)
	port := "8500"
	if pod.Annotations["consul.hashicorp.com/connect-service-port"] == "https" {
		port = "8501"
	}
	newAddr := fmt.Sprintf("%v:%v", hostIP, port)
	t.Log.Info("Setting consul client to: %v", newAddr)
	t.HFlags.SetAddress(newAddr)
	t.Client, err = t.HFlags.APIClient()
	if err != nil {
		t.Log.Error("creating Consul client for address %s: %s", newAddr, err)
	}
	return err
}

// Init handles any handler initialization
func (t *HealthCheckHandler) Init() error {
	return nil
}

// ObjectCreated is called when an object is created
func (t *HealthCheckHandler) ObjectCreated(obj interface{}) error {
	return nil
}

// ObjectDeleted is called when an object is deleted
//
func (t *HealthCheckHandler) ObjectDeleted(obj interface{}) error {
	pod := obj.(*core_v1.Pod)
	consulHealthCheckID := t.getConsulHealthCheckID(pod)
	err := t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("%v", err)
		// TODO: do something
	}
	err = t.deregisterConsulHealthCheck(consulHealthCheckID)
	if err != nil {
		// TODO: how to handle errors here?
		t.Log.Error("%v", err)
	}
	// TODO: Delete the healthcheck if it exists
	t.Log.Info("HealthCheckHandler.ObjectDeleted %v", pod.Status.HostIP, consulHealthCheckID)
	return nil
}

// ObjectUpdated is called when an object is updated
func (t *HealthCheckHandler) ObjectUpdated(objOld, objNew interface{}) error {
	pod := objNew.(*core_v1.Pod)
	t.Log.Info("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
	var err error

	// Health checks for Consul are agent-local and as such we need a client connection
	// to the client agent responsible for this connect-injected Pod.
	// Currently we use hostIP to communicate with the Consul client on a host, this
	// is usually in the form of a call to localhost, in this case the operator is
	// not host-local so we create a new client and connection to the agent on the hostIP
	// where the Pod is located.
	// TODO: alternative methods, this will need to change for agentless
	err = t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("%v", err)
		return err
		// TODO: do something
	}
	// The consulHealthCheckID is the health check ID registered with the Consul agent
	// this is an agent-unique ID to identify the TTL health check we create so we can remove
	// it later
	consulHealthCheckID := t.getConsulHealthCheckID(pod)

	// pod.Status.Conditions is a list of Status types.
	// 'Ready' status refers to the cumulative results of all health checks on the Pod
	// If it is set to 'True' the Pod is considered Healthy and all health checks have passed.
	// If it is set to 'False' the Pod has failed health checks and is not considered healthy
	// enough to pass traffic.
	for _, y := range pod.Status.Conditions {
		if y.Type == "Ready" && y.Status != core_v1.ConditionTrue {
			// Register a failing TTL health check with Consul for this agent+endpoint.
			// This will cause the endpoint to be removed from the Consul dns list and
			// stop sending service traffic to the Pod which has been marked Unready
			// via failed k8s health-check probes.
			err = t.registerConsulHealthCheck(consulHealthCheckID, t.getConsulServiceID(pod))
			if err != nil {
				// TODO: do something?
				t.Log.Error("%v", err)
				return err
			}
			break
		} else {
			if y.Type == "Ready" && y.Status == core_v1.ConditionTrue {
				// de-register the failing TTL
				// This will remove the failing TTL health check against the Consul endpoint,
				// assuming that the service's other health checks are passing,
				// it will result in Consul adding the endpoint to the service's dns list
				// TODO: how to handle out of sync/startup case when the health check didnt already exist ?
				err = t.deregisterConsulHealthCheck(consulHealthCheckID)
				if err != nil {
					// TODO: how to handle errors here?
					t.Log.Error("Error running deregister: %v", err)
					return err
				}
				break
			}
		}
	}
	// TODO: how to drop the client connection cleanly?
	t.Client = nil
	t.Log.Info("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
	return nil
}
