/*
Copyright 2018 The kube-fledged authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package app

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/golang/glog"
	v1alpha3 "github.com/senthilrch/kube-fledged/pkg/apis/kubefledged/v1alpha3"
	clientset "github.com/senthilrch/kube-fledged/pkg/client/clientset/versioned"
	fledgedscheme "github.com/senthilrch/kube-fledged/pkg/client/clientset/versioned/scheme"
	informers "github.com/senthilrch/kube-fledged/pkg/client/informers/externalversions/kubefledged/v1alpha3"
	listers "github.com/senthilrch/kube-fledged/pkg/client/listers/kubefledged/v1alpha3"
	"github.com/senthilrch/kube-fledged/pkg/images"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const controllerAgentName = "kubefledged-controller"
const imageCachePurgeAnnotationKey = "kubefledged.io/purge-imagecache"
const imageCacheRefreshAnnotationKey = "kubefledged.io/refresh-imagecache"

const (
	// SuccessSynced is used as part of the Event 'reason' when a ImageCache is synced
	SuccessSynced = "Synced"
	// MessageResourceSynced is the message used for an Event fired when a ImageCache
	// is synced successfully
	MessageResourceSynced = "ImageCache synced successfully"
)

var (
	defaultNodeLatency = 5 * time.Second
)

// Controller is the controller for ImageCache resources
type Controller struct {
	// kubeclientset is a standard kubernetes clientset
	kubeclientset kubernetes.Interface
	// kubefledgedclientset is a clientset for kubefledged.io API group
	kubefledgedclientset clientset.Interface

	fledgedNameSpace  string
	nodesLister       corelisters.NodeLister
	nodesSynced       cache.InformerSynced
	imageCachesLister listers.ImageCacheLister
	imageCachesSynced cache.InformerSynced

	// workqueue is a rate limited work queue. This is used to queue work to be
	// processed instead of performing it as soon as a change happens. This
	// means we can ensure we only process a fixed amount of resources at a
	// time, and makes it easy to ensure we are never processing the same item
	// simultaneously in two different workers.
	workqueue      workqueue.RateLimitingInterface
	imageworkqueue workqueue.RateLimitingInterface
	imageManager   *images.ImageManager
	// recorder is an event recorder for recording Event resources to the
	// Kubernetes API.
	recorder                   record.EventRecorder
	imageCacheRefreshFrequency time.Duration

	// TODO(gaocegege): Should we use concurrent map?
	nodesCache map[string]bool
}

// NewController returns a new fledged controller
func NewController(
	kubeclientset kubernetes.Interface,
	kubefledgedclientset clientset.Interface,
	namespace string,
	nodeInformer coreinformers.NodeInformer,
	imageCacheInformer informers.ImageCacheInformer,
	imageCacheRefreshFrequency time.Duration,
	imagePullDeadlineDuration time.Duration,
	criClientImage string,
	busyboxImage string,
	imagePullPolicy string,
	serviceAccountName string,
	imageDeleteJobHostNetwork bool,
	jobPriorityClassName string,
	canDeleteJob bool,
	criSocketPath string) *Controller {

	runtime.Must(fledgedscheme.AddToScheme(scheme.Scheme))
	glog.V(4).Info("Creating event broadcaster")
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{Component: controllerAgentName})

	controller := &Controller{
		kubeclientset:              kubeclientset,
		kubefledgedclientset:       kubefledgedclientset,
		fledgedNameSpace:           namespace,
		nodesLister:                nodeInformer.Lister(),
		nodesCache:                 map[string]bool{},
		nodesSynced:                nodeInformer.Informer().HasSynced,
		imageCachesLister:          imageCacheInformer.Lister(),
		imageCachesSynced:          imageCacheInformer.Informer().HasSynced,
		workqueue:                  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ImageCaches"),
		imageworkqueue:             workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "ImagePullerStatus"),
		recorder:                   recorder,
		imageCacheRefreshFrequency: imageCacheRefreshFrequency,
	}

	imageManager, _ := images.NewImageManager(controller.workqueue, controller.imageworkqueue,
		controller.kubeclientset, controller.fledgedNameSpace, imagePullDeadlineDuration,
		criClientImage, busyboxImage, imagePullPolicy, serviceAccountName, imageDeleteJobHostNetwork,
		jobPriorityClassName, canDeleteJob, criSocketPath)
	controller.imageManager = imageManager

	glog.Info("Setting up event handlers")
	// Set up an event handler for when ImageCache resources change
	imageCacheInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			controller.enqueueImageCache(images.ImageCacheCreate, nil, obj)
		},
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueImageCache(images.ImageCacheUpdate, old, new)
		},
		DeleteFunc: func(obj interface{}) {
			controller.enqueueImageCache(images.ImageCacheDelete, obj, nil)
		},
	})

	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			controller.enqueueNode(obj, "add")
		},
		UpdateFunc: func(old, new interface{}) {
			controller.enqueueNode(new, "update")
		},
		DeleteFunc: func(obj interface{}) {
			controller.enqueueNode(obj, "delete")
		},
	})
	return controller
}

func (c *Controller) enqueueNode(obj interface{}, operation string) {
	switch operation {
	case "delete":
		node, ok := obj.(*corev1.Node)
		if !ok {
			tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
			if !ok {
				glog.Errorf("Couldn't get object from tombstone %#v", obj)
				return
			}
			node, ok = tombstone.Obj.(*corev1.Node)
			if !ok {
				glog.Errorf("Tombstone contained object that is not a Node %#v", obj)
				return
			}
		}
		delete(c.nodesCache, node.Name)
	case "add", "update":
		node, ok := obj.(*corev1.Node)
		if !ok {
			tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
			if !ok {
				glog.Errorf("Couldn't get object from tombstone %#v", obj)
				return
			}
			node, ok = tombstone.Obj.(*corev1.Node)
			if !ok {
				glog.Errorf("Tombstone contained object that is not a Node %#v", obj)
				return
			}
		}
		if IsNodeReady(node) {
			if _, ok := c.nodesCache[node.Name]; !ok {
				c.nodesCache[node.Name] = true
				glog.V(4).Infof("Node %s updated and ready", node.Name)
				ics, err := c.imageCachesLister.ImageCaches(c.fledgedNameSpace).List(labels.Everything())
				if err != nil {
					glog.Errorf("Error listing ImageCaches: %s", err.Error())
					return
				}

				// Wait for defaultNodeLatency before enqueuing ImageCaches to make kubernetes api server happy.
				ticker := time.NewTicker(defaultNodeLatency)
				quit := make(chan struct{})
				go func() {
					for {
						select {
						case <-ticker.C:
							glog.V(4).Infof("Enqueuing ImageCaches for node %s", node.Name)
							for _, ic := range ics {
								c.enqueueImageCache(images.ImageCacheRefresh, ic, ic)
							}
							close(quit)
						case <-quit:
							ticker.Stop()
							return
						}
					}
				}()
			}
		}
	}
}

// IsNodeReady checks whether the Node is ready
func IsNodeReady(node *corev1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// PreFlightChecks performs pre-flight checks and actions before the controller is started
func (c *Controller) PreFlightChecks() error {
	if err := c.danglingJobs(); err != nil {
		return err
	}
	if err := c.danglingImageCaches(); err != nil {
		return err
	}
	return nil
}

// danglingJobs finds and removes dangling or stuck jobs
func (c *Controller) danglingJobs() error {
	appEqKubefledged, _ := labels.NewRequirement("app", selection.Equals, []string{"kubefledged"})
	kubefledgedEqImagemanager, _ := labels.NewRequirement("kubefledged", selection.Equals, []string{"kubefledged-image-manager"})
	labelSelector := labels.NewSelector()
	labelSelector = labelSelector.Add(*appEqKubefledged, *kubefledgedEqImagemanager)

	joblist, err := c.kubeclientset.BatchV1().Jobs("").List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector.String(),
	})
	if err != nil {
		glog.Errorf("Error listing jobs: %v", err)
		return err
	}

	if joblist == nil || len(joblist.Items) == 0 {
		glog.Info("No dangling or stuck jobs found...")
		return nil
	}
	deletePropagation := metav1.DeletePropagationBackground
	for _, job := range joblist.Items {
		err := c.kubeclientset.BatchV1().Jobs(job.Namespace).
			Delete(context.TODO(), job.Name, metav1.DeleteOptions{PropagationPolicy: &deletePropagation})
		if err != nil {
			glog.Errorf("Error deleting job(%s): %v", job.Name, err)
			return err
		}
		glog.Infof("Dangling Job(%s) deleted", job.Name)
	}
	return nil
}

// danglingImageCaches finds dangling or stuck image cache and marks them as abhorted. Such
// image caches will get refreshed in the next cycle
func (c *Controller) danglingImageCaches() error {
	dangling := false
	imagecachelist, err := c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		glog.Errorf("Error listing imagecaches: %v", err)
		return err
	}

	if imagecachelist == nil || len(imagecachelist.Items) == 0 {
		glog.Info("No dangling or stuck imagecaches found...")
		return nil
	}
	status := &v1alpha3.ImageCacheStatus{
		Failures: map[string]v1alpha3.NodeReasonMessageList{},
		Status:   v1alpha3.ImageCacheActionStatusAborted,
		Reason:   v1alpha3.ImageCacheReasonImagePullAborted,
		Message:  v1alpha3.ImageCacheMessageImagePullAborted,
	}
	for _, imagecache := range imagecachelist.Items {
		if imagecache.Status.Status == v1alpha3.ImageCacheActionStatusProcessing {
			status.StartTime = imagecache.Status.StartTime
			err := c.updateImageCacheStatus(&imagecache, status)
			if err != nil {
				glog.Errorf("Error updating ImageCache(%s) status to '%s': %v", imagecache.Name, v1alpha3.ImageCacheActionStatusAborted, err)
				return err
			}
			dangling = true
			glog.Infof("Dangling Image cache(%s) status changed to '%s'", imagecache.Name, v1alpha3.ImageCacheActionStatusAborted)
		}
	}

	if !dangling {
		glog.Info("No dangling or stuck imagecaches found...")
	}
	return nil
}

// Run will set up the event handlers for types we are interested in, as well
// as syncing informer caches and starting workers. It will block until stopCh
// is closed, at which point it will shutdown the workqueue and wait for
// workers to finish processing their current work items.
func (c *Controller) Run(threadiness int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer c.workqueue.ShutDown()
	defer c.imageworkqueue.ShutDown()

	// Start the informer factories to begin populating the informer caches
	glog.Info("Starting kubefledged-controller")

	// Wait for the caches to be synced before starting workers
	if ok := cache.WaitForCacheSync(stopCh, c.nodesSynced, c.imageCachesSynced); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}
	glog.Info("Informer caches synched successfull")

	// Launch workers to process ImageCache resources
	for i := 0; i < threadiness; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}
	glog.Info("Image cache worker started")

	if c.imageCacheRefreshFrequency.Nanoseconds() != int64(0) {
		go wait.Until(c.runRefreshWorker, c.imageCacheRefreshFrequency, stopCh)
		glog.Info("Image cache refresh worker started")
	}

	c.imageManager.Run(stopCh)
	if err := c.imageManager.Run(stopCh); err != nil {
		glog.Fatalf("Error running image manager: %s", err.Error())
	}
	glog.Info("Image manager started")

	<-stopCh
	glog.Info("Shutting down workers")

	return nil
}

// enqueueImageCache takes a ImageCache resource and converts it into a namespace/name
// string which is then put onto the work queue. This method should *not* be
// passed resources of any type other than ImageCache.
func (c *Controller) enqueueImageCache(workType images.WorkType, old, new interface{}) bool {
	var key string
	var err error
	var obj interface{}
	wqKey := images.WorkQueueKey{}

	switch workType {
	case images.ImageCacheCreate:
		obj = new
		newImageCache := new.(*v1alpha3.ImageCache)
		// If the ImageCache resource already has a status field, it means it's already
		// synced, so do not queue it for processing
		if !reflect.DeepEqual(newImageCache.Status, v1alpha3.ImageCacheStatus{}) {
			return false
		}
	case images.ImageCacheUpdate:
		obj = new
		oldImageCache := old.(*v1alpha3.ImageCache)
		newImageCache := new.(*v1alpha3.ImageCache)

		if oldImageCache.Status.Status == v1alpha3.ImageCacheActionStatusProcessing {
			if !reflect.DeepEqual(newImageCache.Spec, oldImageCache.Spec) {
				glog.Warningf("Received image cache update/purge/delete for '%s' while it is under processing, so ignoring.", oldImageCache.Name)
				return false
			}
		}
		if _, exists := newImageCache.Annotations[imageCachePurgeAnnotationKey]; exists {
			if _, exists := oldImageCache.Annotations[imageCachePurgeAnnotationKey]; !exists {
				workType = images.ImageCachePurge
				break
			}
		}
		if _, exists := newImageCache.Annotations[imageCacheRefreshAnnotationKey]; exists {
			if _, exists := oldImageCache.Annotations[imageCacheRefreshAnnotationKey]; !exists {
				workType = images.ImageCacheRefresh
				break
			}
		}
		if reflect.DeepEqual(newImageCache.Spec, oldImageCache.Spec) {
			return false
		}
	case images.ImageCacheDelete:
		return false

	case images.ImageCacheRefresh:
		obj = old
	}

	if key, err = cache.MetaNamespaceKeyFunc(obj); err != nil {
		runtime.HandleError(err)
		return false
	}
	wqKey.WorkType = workType
	wqKey.ObjKey = key
	if workType == images.ImageCacheUpdate {
		oldImageCache := old.(*v1alpha3.ImageCache)
		wqKey.OldImageCache = oldImageCache
	}

	c.workqueue.AddRateLimited(wqKey)
	glog.V(4).Infof("enqueueImageCache::ImageCache resource queued for work type %s", workType)
	return true
}

// runWorker is a long-running function that will continually call the
// processNextWorkItem function in order to read and process a message on the
// workqueue.
func (c *Controller) runWorker() {
	for c.processNextWorkItem() {
	}
}

// processNextWorkItem will read a single work item off the workqueue and
// attempt to process it, by calling the syncHandler.
func (c *Controller) processNextWorkItem() bool {
	//glog.Info("processNextWorkItem::Beginning...")
	obj, shutdown := c.workqueue.Get()

	if shutdown {
		return false
	}

	// We wrap this block in a func so we can defer c.workqueue.Done.
	err := func(obj interface{}) error {
		// We call Done here so the workqueue knows we have finished
		// processing this item. We also must remember to call Forget if we
		// do not want this work item being re-queued. For example, we do
		// not call Forget if a transient error occurs, instead the item is
		// put back on the workqueue and attempted again after a back-off
		// period.
		defer c.workqueue.Done(obj)
		var key images.WorkQueueKey
		var ok bool
		// We expect strings to come off the workqueue. These are of the
		// form namespace/name. We do this as the delayed nature of the
		// workqueue means the items in the informer cache may actually be
		// more up to date that when the item was initially put onto the
		// workqueue.
		if key, ok = obj.(images.WorkQueueKey); !ok {
			// As the item in the workqueue is actually invalid, we call
			// Forget here else we'd go into a loop of attempting to
			// process a work item that is invalid.
			c.workqueue.Forget(obj)
			runtime.HandleError(fmt.Errorf("unexpected type in workqueue: %#v", obj))
			return nil
		}
		// Run the syncHandler, passing it the namespace/name string of the
		// ImageCache resource to be synced.
		if err := c.syncHandler(key); err != nil {
			glog.Errorf("error syncing imagecache: %v", err.Error())
			return fmt.Errorf("error syncing imagecache: %v", err.Error())
		}
		// Finally, if no error occurs we Forget this item so it does not
		// get queued again until another change happens.
		c.workqueue.Forget(obj)
		//glog.Infof("Successfully synced '%s' for event '%s'", key.ObjKey, key.WorkType)
		return nil
	}(obj)

	if err != nil {
		runtime.HandleError(err)
		return true
	}

	return true
}

// runRefreshWorker is resposible of refreshing the image cache
func (c *Controller) runRefreshWorker() {
	// List the ImageCache resources
	imageCaches, err := c.imageCachesLister.ImageCaches("").List(labels.Everything())
	if err != nil {
		glog.Errorf("Error in listing image caches: %v", err)
		return
	}
	for i := range imageCaches {
		// Do not refresh if status is not yet updated
		if reflect.DeepEqual(imageCaches[i].Status, v1alpha3.ImageCacheStatus{}) {
			continue
		}
		// Do not refresh if image cache is already under processing
		if imageCaches[i].Status.Status == v1alpha3.ImageCacheActionStatusProcessing {
			continue
		}
		// Do not refresh image cache if cache spec validation failed
		if imageCaches[i].Status.Status == v1alpha3.ImageCacheActionStatusFailed &&
			imageCaches[i].Status.Reason == v1alpha3.ImageCacheReasonCacheSpecValidationFailed {
			continue
		}
		// Do not refresh if image cache has been purged
		if imageCaches[i].Status.Reason == v1alpha3.ImageCacheReasonImageCachePurge {
			continue
		}
		c.enqueueImageCache(images.ImageCacheRefresh, imageCaches[i], nil)
	}
}

// syncHandler compares the actual state with the desired, and attempts to
// converge the two. It then updates the Status block of the ImageCache resource
// with the current status of the resource.
func (c *Controller) syncHandler(wqKey images.WorkQueueKey) error {
	status := &v1alpha3.ImageCacheStatus{
		Failures: map[string]v1alpha3.NodeReasonMessageList{},
	}

	// Convert the namespace/name string into a distinct namespace and name
	namespace, name, err := cache.SplitMetaNamespaceKey(wqKey.ObjKey)
	if err != nil {
		glog.Errorf("Error from cache.SplitMetaNamespaceKey(): %v", err)
		return err
	}

	glog.Infof("Starting to sync image cache %s(%s)", name, wqKey.WorkType)

	switch wqKey.WorkType {
	case images.ImageCacheCreate, images.ImageCacheUpdate, images.ImageCacheRefresh, images.ImageCachePurge:

		startTime := metav1.Now()
		status.StartTime = &startTime
		// Get the ImageCache resource with this namespace/name
		imageCache, err := c.imageCachesLister.ImageCaches(namespace).Get(name)
		if err != nil {
			// The ImageCache resource may no longer exist, in which case we stop
			// processing.
			glog.Errorf("Error getting imagecache(%s): %v", name, err)
			return err
		}

		if wqKey.WorkType == images.ImageCacheUpdate && wqKey.OldImageCache == nil {
			status.Status = v1alpha3.ImageCacheActionStatusFailed
			status.Reason = v1alpha3.ImageCacheReasonOldImageCacheNotFound
			status.Message = v1alpha3.ImageCacheMessageOldImageCacheNotFound

			if err := c.updateImageCacheStatus(imageCache, status); err != nil {
				glog.Errorf("Error updating imagecache status to %s: %v", status.Status, err)
				return err
			}
			glog.Errorf("%s: %s", v1alpha3.ImageCacheReasonOldImageCacheNotFound, v1alpha3.ImageCacheMessageOldImageCacheNotFound)
			return fmt.Errorf("%s: %s", v1alpha3.ImageCacheReasonOldImageCacheNotFound, v1alpha3.ImageCacheMessageOldImageCacheNotFound)
		}

		cacheSpec := imageCache.Spec.CacheSpec
		glog.V(4).Infof("cacheSpec: %+v", cacheSpec)
		var nodes []*corev1.Node

		status.Status = v1alpha3.ImageCacheActionStatusProcessing

		if wqKey.WorkType == images.ImageCacheCreate {
			status.Reason = v1alpha3.ImageCacheReasonImageCacheCreate
			status.Message = v1alpha3.ImageCacheMessagePullingImages
		}

		if wqKey.WorkType == images.ImageCacheUpdate {
			status.Reason = v1alpha3.ImageCacheReasonImageCacheUpdate
			status.Message = v1alpha3.ImageCacheMessageUpdatingCache
		}

		if wqKey.WorkType == images.ImageCacheRefresh {
			status.Reason = v1alpha3.ImageCacheReasonImageCacheRefresh
			status.Message = v1alpha3.ImageCacheMessageRefreshingCache
		}

		if wqKey.WorkType == images.ImageCachePurge {
			status.Reason = v1alpha3.ImageCacheReasonImageCachePurge
			status.Message = v1alpha3.ImageCacheMessagePurgeCache
		}

		imageCache, err = c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			glog.Errorf("Error getting imagecache(%s) from api server: %v", name, err)
			return err
		}

		if err = c.updateImageCacheStatus(imageCache, status); err != nil {
			glog.Errorf("Error updating imagecache status to %s: %v", status.Status, err)
			return err
		}

		for k, i := range cacheSpec {
			if len(i.NodeSelector) > 0 {
				if nodes, err = c.nodesLister.List(labels.Set(i.NodeSelector).AsSelector()); err != nil {
					glog.Errorf("Error listing nodes using nodeselector %+v: %v", i.NodeSelector, err)
					return err
				}
			} else {
				if nodes, err = c.nodesLister.List(labels.Everything()); err != nil {
					glog.Errorf("Error listing nodes using nodeselector labels.Everything(): %v", err)
					return err
				}
			}
			glog.V(4).Infof("No. of nodes in %+v is %d", i.NodeSelector, len(nodes))

			for _, n := range nodes {
				for _, image := range i.Images {
					ipr := images.ImageWorkRequest{
						Image:                   image.Name,
						ForceFullCache:          image.ForceFullCache,
						Node:                    n,
						ContainerRuntimeVersion: n.Status.NodeInfo.ContainerRuntimeVersion,
						WorkType:                wqKey.WorkType,
						Imagecache:              imageCache,
					}
					c.imageworkqueue.AddRateLimited(ipr)
				}
				if wqKey.WorkType == images.ImageCacheUpdate {
					for _, oldimage := range wqKey.OldImageCache.Spec.CacheSpec[k].Images {
						matched := false
						for _, newimage := range i.Images {
							if oldimage == newimage {
								matched = true
								break
							}
						}
						if !matched {
							ipr := images.ImageWorkRequest{
								Image:                   oldimage.Name,
								ForceFullCache:          oldimage.ForceFullCache,
								Node:                    n,
								ContainerRuntimeVersion: n.Status.NodeInfo.ContainerRuntimeVersion,
								WorkType:                images.ImageCachePurge,
								Imagecache:              imageCache,
							}
							c.imageworkqueue.AddRateLimited(ipr)
						}
					}
				}
			}
		}

		// We add an empty image pull request to signal the image manager that all
		// requests for this sync action have been placed in the imageworkqueue
		c.imageworkqueue.AddRateLimited(images.ImageWorkRequest{WorkType: wqKey.WorkType, Imagecache: imageCache})

	case images.ImageCacheStatusUpdate:
		glog.V(4).Infof("wqKey.Status = %+v", wqKey.Status)
		// Finally, we update the status block of the ImageCache resource to reflect the
		// current state of the world
		// Get the ImageCache resource with this namespace/name
		imageCache, err := c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			glog.Errorf("Error getting image cache %s: %v", name, err)
			return err
		}

		if imageCache.Status.StartTime != nil {
			status.StartTime = imageCache.Status.StartTime
		}

		status.Status = v1alpha3.ImageCacheActioneNoImagesPulledOrDeleted
		status.Reason = imageCache.Status.Reason
		status.Message = v1alpha3.ImageCacheMessageNoImagesPulledOrDeleted

		failures := false
		for _, v := range *wqKey.Status {
			if (v.Status == images.ImageWorkResultStatusSucceeded || v.Status == images.ImageWorkResultStatusAlreadyPulled) && !failures {
				status.Status = v1alpha3.ImageCacheActionStatusSucceeded
				if v.ImageWorkRequest.WorkType == images.ImageCachePurge {
					status.Message = v1alpha3.ImageCacheMessageImagesDeletedSuccessfully
				} else {
					status.Message = v1alpha3.ImageCacheMessageImagesPulledSuccessfully
				}
			}
			if (v.Status == images.ImageWorkResultStatusFailed || v.Status == images.ImageWorkResultStatusUnknown) && !failures {
				failures = true
				status.Status = v1alpha3.ImageCacheActionStatusFailed
				if v.ImageWorkRequest.WorkType == images.ImageCachePurge {
					status.Message = v1alpha3.ImageCacheMessageImageDeleteFailedForSomeImages
				} else {
					status.Message = v1alpha3.ImageCacheMessageImagePullFailedForSomeImages
				}
			}
			if v.Status == images.ImageWorkResultStatusFailed || v.Status == images.ImageWorkResultStatusUnknown {
				status.Failures[v.ImageWorkRequest.Image] = append(
					status.Failures[v.ImageWorkRequest.Image], v1alpha3.NodeReasonMessage{
						Node:    v.ImageWorkRequest.Node.Labels["kubernetes.io/hostname"],
						Reason:  v.Reason,
						Message: v.Message,
					})
			}
		}

		err = c.updateImageCacheStatus(imageCache, status)
		if err != nil {
			glog.Errorf("Error updating ImageCache status: %v", err)
			return err
		}

		if imageCache.Status.Reason == v1alpha3.ImageCacheReasonImageCachePurge || imageCache.Status.Reason == v1alpha3.ImageCacheReasonImageCacheRefresh {
			imageCache, err := c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(namespace).Get(context.TODO(), name, metav1.GetOptions{})
			if err != nil {
				glog.Errorf("Error getting image cache %s: %v", name, err)
				return err
			}
			if imageCache.Status.Reason == v1alpha3.ImageCacheReasonImageCachePurge {
				if err := c.removeAnnotation(imageCache, imageCachePurgeAnnotationKey); err != nil {
					glog.Errorf("Error removing Annotation %s from imagecache(%s): %v", imageCachePurgeAnnotationKey, imageCache.Name, err)
					return err
				}
			}
			if imageCache.Status.Reason == v1alpha3.ImageCacheReasonImageCacheRefresh {
				if _, ok := imageCache.Annotations[imageCacheRefreshAnnotationKey]; ok {
					if err := c.removeAnnotation(imageCache, imageCacheRefreshAnnotationKey); err != nil {
						glog.Errorf("Error removing Annotation %s from imagecache(%s): %v", imageCacheRefreshAnnotationKey, imageCache.Name, err)
						return err
					}
				}
			}
		}

		if status.Status == v1alpha3.ImageCacheActionStatusSucceeded || status.Status == v1alpha3.ImageCacheActioneNoImagesPulledOrDeleted {
			c.recorder.Event(imageCache, corev1.EventTypeNormal, status.Reason, status.Message)
		}

		if status.Status == v1alpha3.ImageCacheActionStatusFailed {
			c.recorder.Event(imageCache, corev1.EventTypeWarning, status.Reason, status.Message)
		}
	}
	glog.Infof("Completed sync actions for image cache %s(%s)", name, wqKey.WorkType)
	return nil

}

func (c *Controller) updateImageCacheStatus(imageCache *v1alpha3.ImageCache, status *v1alpha3.ImageCacheStatus) error {
	imageCacheCopy, err := c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(imageCache.Namespace).Get(context.TODO(), imageCache.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	// NEVER modify objects from the store. It's a read-only, local cache.
	// You can use DeepCopy() to make a deep copy of original object and modify this copy
	// Or create a copy manually for better performance
	imageCacheCopy.Status = *status
	if imageCacheCopy.Status.Status != v1alpha3.ImageCacheActionStatusProcessing {
		completionTime := metav1.Now()
		imageCacheCopy.Status.CompletionTime = &completionTime
	}
	// If the CustomResourceSubresources feature gate is not enabled,
	// we must use Update instead of UpdateStatus to update the Status block of the ImageCache resource.
	// UpdateStatus will not allow changes to the Spec of the resource,
	// which is ideal for ensuring nothing other than resource status has been updated.
	_, err = c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(imageCache.Namespace).Update(context.TODO(), imageCacheCopy, metav1.UpdateOptions{})
	return err
}

func (c *Controller) removeAnnotation(imageCache *v1alpha3.ImageCache, annotationKey string) error {
	imageCacheCopy := imageCache.DeepCopy()
	delete(imageCacheCopy.Annotations, annotationKey)
	_, err := c.kubefledgedclientset.KubefledgedV1alpha3().ImageCaches(imageCache.Namespace).Update(context.TODO(), imageCacheCopy, metav1.UpdateOptions{})
	if err == nil {
		glog.Infof("Annotation %s removed from imagecache(%s)", annotationKey, imageCache.Name)
	}
	return err
}
