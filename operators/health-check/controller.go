package healthcheckoperator

import (
	ctx "context"
	"fmt"
	"reflect"
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
type Controller struct {
	Log        log.Logger
	Clientset  kubernetes.Interface
	Queue      workqueue.RateLimitingInterface
	Informer   cache.SharedIndexInformer
	Handle     Handler
	MaxRetries int
}

func (c *Controller) setupInformer() error {
	c.Informer = cache.NewSharedIndexInformer(
		// the ListWatch contains two different functions that our
		// informer requires: ListFunc to take care of listing and watching
		// the resources we want to handle
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = "consul" //=consul.hashicorp.com/connect-inject"
				// list all of the pods (core resource) in the default namespace
				// TODO: Find where we store k8sAllowNamespaces and how to watch only that
				return c.Clientset.CoreV1().Pods(metav1.NamespaceAll).List(ctx.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = "consul" //=consul.hashicorp.com/connect-inject"
				// watch all of the pods which match Consul labels in all namespaces
				return c.Clientset.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx.Background(), options)
			},
		},
		&corev1.Pod{}, // the target type (Pod)
		0,             // no resync (period of 0)
		cache.Indexers{},
	)
	return nil
}

func (c *Controller) setupWorkQueue() {
	// create a new queue so that when the informer gets a resource that is either
	// a result of listing or watching, we can add an idenfitying key to the queue
	// so that it can be handled in the handler
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
			// we are just doing it in the format of 'namespace/name')
			key, err := cache.MetaNamespaceKeyFunc(obj)
			c.Log.Info("Add pod: %s", key)
			if err == nil {
				// TODO: do not queue a queue for Create because it is a no-op anyway
				// add the key to the queue for the handler to get
				c.Queue.Add(key)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			// TODO: Right now we're catching the pod update but we're unable to see that its an update via the Key
			newPod := newObj.(*corev1.Pod)
			oldPod := oldObj.(*corev1.Pod)

			if reflect.DeepEqual(oldObj, newObj) == false {
				c.Log.Info("======== Deployment Updated: " + newPod.Name)
			} else {
				c.Log.Info("========= not updated" + newPod.Name)
			}
			// Logic is as follows:
			// We will only queue events which satisfy the condition of a pod Status Condition change from Ready/NotReady
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
				key, err := cache.MetaNamespaceKeyFunc(newObj)
				c.Log.Info("Update pod: %s", key)
				if err == nil {
					c.Queue.Add(key)
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
			c.Log.Info("Delete pod: %s", key)
			if err == nil {
				c.Queue.Add(key)
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

	c.Log.Info("Controller.Run: initiating")

	// run the Informer to start listing and watching resources
	go c.Informer.Run(stopCh)

	// do the initial synchronization (one time) to populate resources
	if !cache.WaitForCacheSync(stopCh, c.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("Error syncing cache"))
		return
	}
	c.Log.Info("Controller.Run: cache sync complete")

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
	c.Log.Info("Controller.runWorker: starting")

	// invoke processNextItem to fetch and consume the next change
	// to a watched or listed resource
	for c.processNextItem() {
		c.Log.Info("Controller.runWorker: processing next item")
	}

	c.Log.Info("Controller.runWorker: completed")
}

// processNextItem retrieves each Queued item and takes the
// necessary Handle action based off of if the item was
// created or deleted
func (c *Controller) processNextItem() bool {
	c.Log.Info("Controller.processNextItem: start")

	// fetch the next item (blocking) from the Queue to process or
	// if a shutdown is requested then return out of this to stop
	// processing
	key, quit := c.Queue.Get()

	// stop the worker loop from running as this indicates we
	// have sent a shutdown message that the Queue has indicated
	// from the Get method
	if quit {
		return false
	}

	// assert the string out of the key (format `namespace/name`)
	keyRaw := key.(string)

	// take the string key and get the object out of the indexer
	//
	// item will contain the complex object for the resource and
	// exists is a bool that'll indicate whether or not the
	// resource was created (true) or deleted (false)
	//
	// if there is an error in getting the key from the index
	// then we want to retry this particular Queue key a certain
	// number of times (5 here) before we forget the Queue key
	// and throw an error
	item, exists, err := c.Informer.GetIndexer().GetByKey(keyRaw)
	if err != nil {
		// TODO: fix this retry number
		if c.Queue.NumRequeues(key) < 5 {
			c.Log.Error("Controller.processNextItem: Failed processing item with key %s with error %v, retrying", key, err)
			c.Queue.AddRateLimited(key)
		} else {
			c.Log.Error("Controller.processNextItem: Failed processing item with key %s with error %v, no more retries", key, err)
			c.Queue.Forget(key)
			utilruntime.HandleError(err)
		}
	}

	// if the item doesn't exist then it was deleted and we need to fire off the Handle's
	// ObjectDeleted method. but if the object does exist that indicates that the object
	// was created (or updated) so run the ObjectCreated method
	//
	// after both instances, we want to forget the key from the Queue, as this indicates
	// a code path of successful Queue key processing
	if !exists {
		c.Log.Info("Controller.processNextItem: object deleted detected: %s", keyRaw)
		err = c.Handle.ObjectDeleted(item)
		if err == nil {
			c.Queue.Forget(key)
		} else if c.Queue.NumRequeues(key) < c.MaxRetries {
			c.Log.Error("Unable to process request, retrying")
			c.Queue.AddRateLimited(key)
		}
	} else {
		// This is a Pod Status Update
		c.Log.Info("Controller.processNextItem: object update detected: %s", keyRaw)
		err = c.Handle.ObjectUpdated(nil, item)
		if err == nil {
			c.Queue.Forget(key)
		} else if c.Queue.NumRequeues(key) < c.MaxRetries {
			c.Log.Error("Unable to process request, retrying")
			c.Queue.AddRateLimited(key)
		}
	}
	if err == nil {
		c.Queue.Done(key)
	}
	// keep the worker loop running by returning true
	return true
}
