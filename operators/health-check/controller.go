package healthcheckoperator

import (
	ctx "context"
	"fmt"
	"reflect"
	"strings"
	"time"

	log "github.com/hashicorp/go-hclog"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Controller struct defines how a controller should encapsulate
// logging, client connectivity, informing (list and watching)
// queueing, and handling of resource changes
// TODO: right now we only support a single namespace or "metav1.NamespaceAll"
type Controller struct {
	Log        log.Logger
	Clientset  kubernetes.Interface
	Queue      workqueue.RateLimitingInterface
	Informer   cache.SharedIndexInformer
	Handle     Handler
	MaxRetries int
	Namespace  string
}

func (c *Controller) setupInformer() {
	c.Informer = cache.NewSharedIndexInformer(
		// the ListWatch contains two different functions that our
		// informer requires: ListFunc to take care of listing and watching
		// the resources we want to handle
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = "consul" //=consul.hashicorp.com/connect-inject"
				// list all of the pods (core resource) in the k8s namespace
				return c.Clientset.CoreV1().Pods(c.Namespace).List(ctx.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = "consul" //=consul.hashicorp.com/connect-inject"
				// watch all of the pods which match Consul labels in k8s namespace
				return c.Clientset.CoreV1().Pods(c.Namespace).Watch(ctx.Background(), options)
			},
		},
		&corev1.Pod{}, // the target type (Pod)
		0,             // no resync (period of 0)
		cache.Indexers{},
	)
}

func (c *Controller) setupWorkQueue() {
	// create a new queue so that when the informer gets a resource that is either
	// a result of listing or watching, we can add an idenfitying key to the queue
	// so that it can be handled in the handler
	// The queue will be indexed via keys in the format of :  OPTION/namespace/resource
	// where OPTION will be one of ADD/UPDATE/CREATE
	c.Queue = workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
}

func (c *Controller) addEventHandlers() {
	// add event handlers to handle the three types of events for resources:
	//  - adding new resources
	//  - updating existing resources
	//  - deleting resources
	c.Informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// convert the resource object into a key (in this case
			// we are just doing it in the format of 'ADD/namespace/name')
			key, err := cache.MetaNamespaceKeyFunc(obj)
			c.Log.Info("Add pod: %s", key)
			if err == nil {
				c.Queue.Add("ADD/" + key)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			newPod := newObj.(*corev1.Pod)
			oldPod := oldObj.(*corev1.Pod)

			if reflect.DeepEqual(oldObj, newObj) == false {
				c.Log.Debug("pod was updated : " + newPod.Name)
			} else {
				c.Log.Debug("pod was not updated " + newPod.Name)
				return
			}
			// Logic is as follows:
			// We will only queue events which satisfy the condition of a pod Status Condition
			// change from Ready/NotReady
			oldPodStatus := corev1.ConditionTrue
			newPodStatus := corev1.ConditionTrue
			for _, y := range oldPod.Status.Conditions {
				if y.Type == "Ready" {
					oldPodStatus = y.Status
				}
			}
			for _, y := range newPod.Status.Conditions {
				if y.Type == "Ready" {
					newPodStatus = y.Status
				}
			}
			// If the Pod Status has changed, we queue the NewObj and we will know based on the condition status
			// whether or not this is an update TO or FROM healthy in the event handler
			if oldPodStatus != newPodStatus {
				// TODO: investigate whether or not the Pod starts up unhealthy and migrates healthy
				// TODO: and be sure that the initial healthy doesnt try to delete a health check that doesnt exist
				key, err := cache.MetaNamespaceKeyFunc(newObj)
				c.Log.Debug("Update pod: %s", key)
				if err == nil {
					c.Queue.Add("UPDATE/" + key)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			// DeletionHandlingMetaNamespaceKeyFunc is a helper function that allows
			// us to check the DeletedFinalStateUnknown existence in the event that
			// a resource was deleted but it is still contained in the index
			//
			// this then in turn calls MetaNamespaceKeyFunc
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			c.Log.Debug("Delete pod: %s", key)
			if err == nil {
				c.Queue.Add("DELETE/" + key)
			}
		},
	})
}

// Run is the main path of execution for the controller loop
func (c *Controller) Run(stopCh <-chan struct{}) {
	// We choose to setup the informer in the Run() section of controller loop so we're not creating it early
	c.setupInformer()
	// Next setup the work queue
	c.setupWorkQueue()
	// Next add eventHandlers
	c.addEventHandlers()

	// handle a panic with logging and exiting
	defer utilruntime.HandleCrash()
	// ignore new items in the Queue but when all goroutines
	// have completed existing items then shutdown
	defer c.Queue.ShutDown()

	c.Log.Debug("Controller.Run: initiating")

	// run the Informer to start listing and watching resources
	go c.Informer.Run(stopCh)

	// do the initial synchronization (one time) to populate resources
	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("error syncing cache"))
		return
	}
	c.Log.Debug("Controller.Run: cache sync complete")

	// run the runWorker method every second with a stop channel
	wait.Until(c.runWorker, time.Second, stopCh)
}

// HasSynced allows us to satisfy the Controller interface
// by wiring up the Informer's HasSynced method to it
func (c *Controller) HasSynced() bool {
	return c.Informer.HasSynced()
}

// runWorker executes the loop to process new items added to the Queue
func (c *Controller) runWorker() {
	c.Log.Debug("Controller.runWorker: starting")

	// invoke processNextItem to fetch and consume the next change
	// to a watched or listed resource
	for c.processNextItem() {
		c.Log.Debug("Controller.runWorker: processing next item")
	}

	c.Log.Debug("Controller.runWorker: completed")
}

// processNextItem retrieves each Queued item and takes the
// necessary Handle action based off of if the item was
// created or deleted
func (c *Controller) processNextItem() bool {
	c.Log.Debug("Controller.processNextItem: start")

	// fetch the next item (blocking) from the Queue to process or
	// if a shutdown is requested then return out of this to stop
	// processing
	key, quit := c.Queue.Get()
	if quit {
		return false
	}

	// assert the string out of the key (format `namespace/name`)
	// Format is as follows :  create/namespace/name, delete/namespace/name, update/namespace/name
	// Also keep track if this is an Add
	create := true
	formattedKey := strings.Split(key.(string), "/")
	if formattedKey[0] != "ADD" {
		create = false
	}
	keyRaw := strings.Join(formattedKey[1:], "/")

	// take the string key and get the object out of the indexer
	//
	// item will contain the complex object for the resource and
	// exists is a bool that'll indicate whether or not the
	// resource was created (true) or deleted (false)
	//
	// if there is an error in getting the key from the index
	// then we want to retry this particular Queue key a certain
	// number of times (10 here) before we forget the Queue key
	// and throw an error
	item, exists, err := c.Informer.GetIndexer().GetByKey(keyRaw)
	if err != nil {
		if c.Queue.NumRequeues(key) < c.MaxRetries {
			c.Log.Info("controller.processNextItem: Failed processing item with key %s with error %v, retrying", key, err)
			c.Queue.AddRateLimited(key)
		} else {
			c.Log.Error("controller.processNextItem: Failed processing item with key %s with error %v, no more retries", key, err)
			c.Queue.Forget(key)
			utilruntime.HandleError(err)
		}
	}

	// if the item doesn't exist then it was deleted and we need to fire off the Handle's
	// ObjectDeleted method. but if the object does exist that indicates that the object
	// was created (or updated) so run the ObjectUpdated method
	//
	// after both instances, we want to forget the key from the Queue, as this indicates
	// a code path of successful Queue key processing
	if !exists {
		// TODO: item is nil in case that !exists?
		c.Log.Info("controller.processNextItem: object deleted detected: %s", keyRaw)
		err = c.Handle.ObjectDeleted(item)
		if err == nil {
			c.Queue.Forget(key)
		} else if c.Queue.NumRequeues(key) < c.MaxRetries {
			c.Log.Error("unable to process request, retrying")
			c.Queue.AddRateLimited(key)
		}
	} else {
		// This is a Pod Create
		if create == true {
			c.Log.Info("controller.processNextItem: object create detected: %s", keyRaw)
			err = c.Handle.ObjectCreated(formattedKey[1], formattedKey[2])
		} else {
			// This is a Pod Status Update
			c.Log.Info("controller.processNextItem: object update detected: %s", keyRaw)
			err = c.Handle.ObjectUpdated(item)
		}
		if err == nil {
			c.Queue.Forget(key)
		} else if c.Queue.NumRequeues(key) < c.MaxRetries {
			c.Log.Error("unable to process request, retrying")
			c.Queue.AddRateLimited(key)
		}
	}
	if err == nil {
		c.Queue.Done(key)
	}
	// keep the worker loop running by returning true
	return true
}
