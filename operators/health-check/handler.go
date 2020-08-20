package healthcheckoperator

// https://github.com/trstringer/k8s-controller-core-resource

import (
	ctx "context"
	"flag"
	"fmt"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/hashicorp/consul-k8s/subcommand/flags"
	"github.com/hashicorp/consul/api"
	log "github.com/hashicorp/go-hclog"
	corev1 "k8s.io/api/core/v1"
)

// Handler interface contains the methods that are required
type Handler interface {
	Init() error
	ObjectCreated(namespace, podname string) error
	ObjectDeleted(obj interface{}) error
	ObjectUpdated(objNew interface{}) error
}

// HealthCheckHandler is a sample implementation of Handler
type HealthCheckHandler struct {
	Log                log.Logger
	AclConfig          api.NamespaceACLConfig
	Client             *api.Client
	Flags              *flag.FlagSet
	HFlags             *flags.HTTPFlags
	Clientset          kubernetes.Interface
	ConsulClientScheme string
}

// getConsulHealthCheckID deterministically generates a health check ID that will be unique to the Agent
// where the health check is registered and deregistered.
func (t *HealthCheckHandler) getConsulHealthCheckID(pod *corev1.Pod) string {
	return t.getConsulServiceID(pod) + "-kubernetes-health-check-ttl"
}

// return the serviceID of the connect service
func (t *HealthCheckHandler) getConsulServiceID(pod *corev1.Pod) string {
	return pod.Name + "-" + pod.Annotations["consul.hashicorp.com/connect-service"]
}

// registerConsulHealthCheck registers a Failing TTL health check for the service on this Agent.
// The Agent is local to the Pod which has failed a health check.
// This has the effect of marking the endpoint unhealthy for Consul service mesh traffic.
// TODO: we no longer use this approach but its usable in the future
func (t *HealthCheckHandler) registerConsulHealthCheck(consulHealthCheckID, serviceID, reason string) error {
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
		// We were unable to find the service on this host, this is due to :
		// 1. the pod is no longer on this pod, has moved or was deregistered from the Agent by Consul
		// 2. Consul isn't working properly
		// 3. Talking to the wrong Agent (unlikely), or this Agent has restarted and forgotten its registrations
		return err
	}
	// Now create a failing TTL health check in Consul associated with this service.
	err = t.Client.Agent().CheckRegister(&api.AgentCheckRegistration{
		Name:      consulHealthCheckID,
		Notes:     "Kubernetes Health Check Failure : " + reason,
		ServiceID: serviceID,
		AgentServiceCheck: api.AgentServiceCheck{
			TTL:                            "10000h",
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
		t.Log.Error("unable to register health check with Consul from k8s: %v", err)
		return err
	}
	return nil
}

// deregisterConsulHealthCheck deregisters a health check for the service on this Agent.
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

// updateConsulClient updates the Consul Client metadata to point to a new hostIP:port
// which is the IP of the host that the Pod runs on, in order to make Agent calls locally
// for health check registration/deregistration.
// TODO: submit PR for HTTPFlags.SetAddress API
// TODO: figure out how to pass agent-port rather than use 8500/8501 based on annotations .. t.Flags.Lookup("agent-port").Name) ??
func (t *HealthCheckHandler) updateConsulClient(pod *corev1.Pod) error {
	var err error
	hostIP := pod.Status.HostIP
	port := "8500"
	if pod.Annotations["consul.hashicorp.com/connect-service-port"] == "https" {
		port = "8501"
	}
	newAddr := fmt.Sprintf("%v:%v", hostIP, port)
	t.HFlags.SetAddress(newAddr)
	// Set client api to point to the new host IP
	t.Log.Debug("setting consul client to: %v", newAddr)
	t.Client, err = t.HFlags.APIClient()
	if err != nil {
		t.Log.Error("unable to get Consul API Client for address %s: %s", newAddr, err)
	}
	return err
}

// Init handles any handler initialization and is a no-op
func (t *HealthCheckHandler) Init() error {
	return nil
}

// registerPassingConsulHealthCheck registers a Failing TTL health check for the service on this Agent.
// The Agent is local to the Pod which has failed a health check.
// This has the effect of marking the endpoint unhealthy for Consul service mesh traffic.
func (t *HealthCheckHandler) registerPassingConsulHealthCheck(consulHealthCheckID, serviceID, reason string) error {
	t.Log.Info("registerPassingHealthCheck, %v %v", consulHealthCheckID, serviceID)
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
		// We were unable to find the service on this host, this is due to :
		// 1. the pod is no longer on this pod, has moved or was deregistered from the Agent by Consul
		// 2. Consul isn't working properly
		// 3. Talking to the wrong Agent (unlikely), or this Agent has restarted and forgotten its registrations
		return err
	}
	// Now create a failing TTL health check in Consul associated with this service.
	err = t.Client.Agent().CheckRegister(&api.AgentCheckRegistration{
		Name:      consulHealthCheckID,
		Notes:     "Kubernetes Health Check " + reason,
		ServiceID: serviceID,
		AgentServiceCheck: api.AgentServiceCheck{
			TTL:                            "10000h",
			Status:                         "passing",
			Notes:                          "",
			TLSSkipVerify:                  true,
			SuccessBeforePassing:           1,
			FailuresBeforeCritical:         1,
			DeregisterCriticalServiceAfter: "",
		},
		Namespace: "",
	})
	if err != nil {
		t.Log.Error("unable to register health check with Consul from k8s: %v", err)
		return err
	}
	return nil
}

// setConsulHealthCheckStatus will update the TTL status of the check
func (t *HealthCheckHandler) setConsulHealthCheckStatus(healthCheckID, reason string, fail bool) error {
	if fail == true {
		return t.Client.Agent().FailTTL(healthCheckID, reason)
	} else {
		return t.Client.Agent().PassTTL(healthCheckID, reason)
	}
}

// ObjectCreated is called when a Pod is created.
// We will now register a TTL health check with the Consul Agent
func (t *HealthCheckHandler) ObjectCreated(namespace, podname string) error {
	var err error
	var pod *corev1.Pod
	retries := 0
	err = backoff.Retry(func() error {
		if retries > 30 {
			err = fmt.Errorf("did not find hostIP for %v", namespace, podname)
			return nil
		}
		retries++
		pod, err = t.Clientset.CoreV1().Pods(namespace).Get(ctx.TODO(), podname, metav1.GetOptions{})
		if err == nil {
			if pod.Status.HostIP != "" {
				return nil
			}
			err = fmt.Errorf("did not find hostIP for %v", namespace, podname)
		}
		return err
	}, backoff.NewConstantBackOff(1*time.Second))
	if err != nil {
		return err
	}
	consulHealthCheckID := t.getConsulHealthCheckID(pod)
	err = t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("unable to update Consul client: %v", err)
		return err
	}

	err = t.registerPassingConsulHealthCheck(consulHealthCheckID, t.getConsulServiceID(pod), "") // y.Reason)
	if err != nil {
		t.Log.Error("unable to register health check: %v", err)
		return err
	}
	t.Log.Info("HealthCheckHandler.ObjectCreated %v", podname, consulHealthCheckID)
	return err
}

// ObjectDeleted is called when an object is deleted, in theory there may exist a race
// condition where the Pod is deleted by kubernetes and prior to this event being received
// Consul has already deregistered it, so skip 400 errors. In the case of the service not
// being found this is a rare op and issuing the delete doesnt hurt anything.
func (t *HealthCheckHandler) ObjectDeleted(obj interface{}) error {
	pod := obj.(*corev1.Pod)
	consulHealthCheckID := t.getConsulHealthCheckID(pod)

	t.Log.Debug("HealthCheckHandler.ObjectDeleted %v", pod.Status.HostIP, consulHealthCheckID)

	err := t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("unable to update Consul client: %v", err)
		return err
	}
	err = t.deregisterConsulHealthCheck(consulHealthCheckID)
	if err != nil {
		// TODO: handle 500 errors, skip 400 errors
		t.Log.Error("unable to deregister health check: %v", err)
	}
	return nil
}

// ObjectUpdated will determine if this is an update to healthy or unhealthy, we have already
// filtered out if this is a non health related transitional update in the controller so all
// updates here are actionable.
// In the case of a transition TO healthy we mark the TTL as passing
// In the case of transition FROM healthy we mark the TTL as failing
func (t *HealthCheckHandler) ObjectUpdated(objNew interface{}) error {
	pod := objNew.(*corev1.Pod)
	t.Log.Debug("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
	err := t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("unable to update Consul client: %v", err)
		return err
	}
	// This is the health check ID that we will update
	consulHealthCheckID := t.getConsulHealthCheckID(pod)

	for _, y := range pod.Status.Conditions {
		if y.Type == "Ready" && y.Status != corev1.ConditionTrue {
			// Set the status of the TTL health check to failed!
			err = t.setConsulHealthCheckStatus(consulHealthCheckID, y.Reason, true)
			if err != nil {
				t.Log.Error("unable to update health check to fail: %v", err)
				return err
			}
			break
		} else {
			if y.Type == "Ready" && y.Status == corev1.ConditionTrue {
				// Set the Consul TTL to passing for this Pod
				err = t.setConsulHealthCheckStatus(consulHealthCheckID, y.Reason, false)
				if err != nil {
					t.Log.Error("unable to update health check to pass: %v", err)
					return err
				}
				break
			}
		}
	}
	// TODO: how to drop the client connection cleanly?
	t.Client = nil
	t.Log.Debug("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
	return nil
}

/*
// ObjectUpdated is called when an object is updated
// This occurs anytime there is a specific transition from Pod healthy/unhealthy
// to unhealthy/healthy and is driven off a queue managed by the controller.
// Since we've guaranteed this is a transition we only care about the "to" state which is the objNew PodCondition.
// As such objOld will always be nil.
//TODO: fix the interface so we dont need to pass objOld
func (t *HealthCheckHandler) ObjectUpdated(objOld, objNew interface{}) error {
	pod := objNew.(*corev1.Pod)
	t.Log.Debug("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)

	// Health checks for Consul are agent-local and as such we need a client connection
	// to the client agent responsible for this connect-injected Pod.
	// Currently we use hostIP to communicate with the Consul client on a host, this
	// is usually in the form of a call to localhost, in this case the operator is
	// not host-local so we create a new client and connection to the agent on the hostIP
	// where the Pod is located.
	// TODO: alternative methods, this will need to change for agentless
	err := t.updateConsulClient(pod)
	if err != nil {
		t.Log.Error("unable to update Consul client: %v", err)
		return err
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
		if y.Type == "Ready" && y.Status != corev1.ConditionTrue {
			// Register a failing TTL health check with Consul for this agent+endpoint.
			// This will cause the endpoint to be removed from the Consul dns list and
			// stop sending service traffic to the Pod which has been marked Unready
			// via failed k8s health-check probes.
			err = t.registerConsulHealthCheck(consulHealthCheckID, t.getConsulServiceID(pod), y.Reason)
			if err != nil {
				t.Log.Error("unable to register health check: %v", err)
				return err
			}
			break
		} else {
			if y.Type == "Ready" && y.Status == corev1.ConditionTrue {
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
	t.Log.Debug("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
	return nil
}
*/
