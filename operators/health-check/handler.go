package healthcheckoperator

// https://github.com/trstringer/k8s-controller-core-resource

import (
	"flag"
	"fmt"
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
	ObjectCreated(obj interface{}) error
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

// TODO: What about namespaces?
// getConsulHealthCheckID deterministically generates a health check ID that will be unique to the Agent
// where the health check is registered and deregistered.
func (t *HealthCheckHandler) getConsulHealthCheckID(pod *corev1.Pod) string {
	return t.getConsulServiceID(pod) + "-kubernetes-health-check-ttl"
}

// return the serviceID of the connect service
func (t *HealthCheckHandler) getConsulServiceID(pod *corev1.Pod) string {
	return pod.Name + "-" + pod.Annotations["consul.hashicorp.com/connect-service"]
}

// deregisterConsulHealthCheck deregisters a health check for the service on this Agent.
// The Agent is local to the Pod which has a succeeding health check.
// This has the effect of marking the endpoint healthy for Consul mesh traffic.
func (t *HealthCheckHandler) deregisterConsulHealthCheck(consulHealthCheckID string) error {
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

// registerPassingConsulHealthCheck registers a Failing TTL health check for the service on this Agent.
// The Agent is local to the Pod which has failed a health check.
// This has the effect of marking the endpoint unhealthy for Consul service mesh traffic.
func (t *HealthCheckHandler) registerPassingConsulHealthCheck(consulHealthCheckID, serviceID, reason string) error {
	t.Log.Error("registerPassingHealthCheck, %v %v", consulHealthCheckID, serviceID)
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

	// Now create a TTL health check in Consul associated with this service.
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

// Init handles any handler initialization and is a no-op
func (t *HealthCheckHandler) Init() error {
	return nil
}

// ObjectCreated is called when a Pod transitions from Pending to Running
// A TTL
//func (t *HealthCheckHandler) ObjectCreated(namespace, podname string) error {
func (t *HealthCheckHandler) ObjectCreated(obj interface{}) error {
	var err error
	pod := obj.(*corev1.Pod)

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
	t.Log.Info("HealthCheckHandler.ObjectCreated %v", pod.Name, consulHealthCheckID)
	return err
}

// ObjectDeleted is called when an object is deleted, in theory there may exist a race
// condition where the Pod is deleted by kubernetes and prior to this event being received
// Consul has already deregistered it, so skip 400/500 errors. In the case of the service not
// being found this is a rare op and issuing the delete doesnt hurt anything.
// TODO: The legwork for this is currently being done by the connect-inject webhook so we're not
// actually calling this at all.
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
	t.Log.Error("HealthCheckHandler.ObjectUpdated, %v %v", pod.Status.HostIP, pod.Status.Conditions)
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
			err = t.setConsulHealthCheckStatus(consulHealthCheckID, y.Message, true)
			if err != nil {
				t.Log.Error("unable to update health check to fail: %v", err)
				return err
			}
			break
		} else {
			if y.Type == "Ready" && y.Status == corev1.ConditionTrue {
				// Set the Consul TTL to passing for this Pod
				err = t.setConsulHealthCheckStatus(consulHealthCheckID, y.Message, false)
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
