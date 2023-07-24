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
	"fmt"
	"strings"
	"testing"
	"time"

	kubefledgedv1alpha3 "github.com/senthilrch/kube-fledged/pkg/apis/kubefledged/v1alpha3"
	clientset "github.com/senthilrch/kube-fledged/pkg/client/clientset/versioned"
	kubefledgedclientsetfake "github.com/senthilrch/kube-fledged/pkg/client/clientset/versioned/fake"
	informers "github.com/senthilrch/kube-fledged/pkg/client/informers/externalversions"
	kubefledgedinformers "github.com/senthilrch/kube-fledged/pkg/client/informers/externalversions/kubefledged/v1alpha3"
	"github.com/senthilrch/kube-fledged/pkg/images"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubeinformers "k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	core "k8s.io/client-go/testing"
)

const fledgedNameSpace = "kube-fledged"

var node = corev1.Node{
	ObjectMeta: metav1.ObjectMeta{
		Labels: map[string]string{"kubernetes.io/hostname": "bar"},
	},
}

// noResyncPeriodFunc returns 0 for resyncPeriod in case resyncing is not needed.
func noResyncPeriodFunc() time.Duration {
	return 0
}

func newTestController(kubeclientset kubernetes.Interface, fledgedclientset clientset.Interface) (*Controller, coreinformers.NodeInformer, kubefledgedinformers.ImageCacheInformer) {
	kubeInformerFactory := kubeinformers.NewSharedInformerFactory(kubeclientset, noResyncPeriodFunc())
	fledgedInformerFactory := informers.NewSharedInformerFactory(fledgedclientset, noResyncPeriodFunc())
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()
	imagecacheInformer := fledgedInformerFactory.Kubefledged().V1alpha3().ImageCaches()
	imageCacheRefreshFrequency := time.Second * 0
	imagePullDeadlineDuration := time.Second * 5
	criClientImage := "senthilrch/fledged-docker-client:latest"
	busyboxImage := "busybox:latest"
	imagePullPolicy := "IfNotPresent"
	serviceAccountName := "sa-kube-fledged"
	imageDeleteJobHostNetwork := false
	jobPriorityClassName := "priority-class-kube-fledged"
	canDelete := false
	socketPath := ""

	/* 	startInformers := true
	   	if startInformers {
	   		stopCh := make(chan struct{})
	   		defer close(stopCh)
	   		kubeInformerFactory.Start(stopCh)
	   		fledgedInformerFactory.Start(stopCh)
	   	} */

	controller := NewController(kubeclientset,
		fledgedclientset, fledgedNameSpace, nodeInformer, imagecacheInformer,
		imageCacheRefreshFrequency, imagePullDeadlineDuration, criClientImage,
		busyboxImage, imagePullPolicy, serviceAccountName, imageDeleteJobHostNetwork,
		jobPriorityClassName, canDelete, socketPath)
	controller.nodesSynced = func() bool { return true }
	controller.imageCachesSynced = func() bool { return true }
	return controller, nodeInformer, imagecacheInformer
}

func TestPreFlightChecks(t *testing.T) {
	tests := []struct {
		name                  string
		jobList               *batchv1.JobList
		jobListError          error
		jobDeleteError        error
		imageCacheList        *kubefledgedv1alpha3.ImageCacheList
		imageCacheListError   error
		imageCacheUpdateError error
		expectErr             bool
		errorString           string
	}{
		{
			name:                  "#1: No dangling jobs. No imagecaches",
			jobList:               &batchv1.JobList{Items: []batchv1.Job{}},
			jobListError:          nil,
			jobDeleteError:        nil,
			imageCacheList:        &kubefledgedv1alpha3.ImageCacheList{Items: []kubefledgedv1alpha3.ImageCache{}},
			imageCacheListError:   nil,
			imageCacheUpdateError: nil,
			expectErr:             false,
			errorString:           "",
		},
		{
			name:           "#2: No dangling jobs. No dangling imagecaches",
			jobList:        &batchv1.JobList{Items: []batchv1.Job{}},
			jobListError:   nil,
			jobDeleteError: nil,
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
						},
					},
				},
			},
			imageCacheListError:   nil,
			imageCacheUpdateError: nil,
			expectErr:             false,
			errorString:           "",
		},
		{
			name: "#3: One dangling job. One dangling image cache. Successful list and delete",
			//imageCache:    nil,
			jobList: &batchv1.JobList{
				Items: []batchv1.Job{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
					},
				},
			},
			jobListError:   nil,
			jobDeleteError: nil,
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
						},
					},
				},
			},
			imageCacheListError:   nil,
			imageCacheUpdateError: nil,
			expectErr:             false,
			errorString:           "",
		},
		{
			name:           "#4: Unsuccessful listing of jobs",
			jobList:        nil,
			jobListError:   fmt.Errorf("fake error"),
			jobDeleteError: nil,
			expectErr:      true,
			errorString:    "Internal error occurred: fake error",
		},
		{
			name:                  "#5: Unsuccessful listing of imagecaches",
			jobList:               &batchv1.JobList{Items: []batchv1.Job{}},
			jobListError:          nil,
			jobDeleteError:        nil,
			imageCacheList:        nil,
			imageCacheListError:   fmt.Errorf("fake error"),
			imageCacheUpdateError: nil,
			expectErr:             true,
			errorString:           "Internal error occurred: fake error",
		},
		{
			name: "#6: One dangling job. Successful list. Unsuccessful delete",
			jobList: &batchv1.JobList{
				Items: []batchv1.Job{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
							Labels: map[string]string{
								"app":         "kubefledged",
								"kubefledged": "kubefledged-image-manager",
							},
						},
					},
				},
			},
			jobListError:   nil,
			jobDeleteError: fmt.Errorf("fake error"),
			expectErr:      true,
			errorString:    "Internal error occurred: fake error",
		},
		{
			name:           "#7: One dangling image cache. Successful list. Unsuccessful delete",
			jobList:        &batchv1.JobList{Items: []batchv1.Job{}},
			jobListError:   nil,
			jobDeleteError: nil,
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
						},
					},
				},
			},
			imageCacheListError:   nil,
			imageCacheUpdateError: fmt.Errorf("fake error"),
			expectErr:             true,
			errorString:           "Internal error occurred: fake error",
		},
	}
	for _, test := range tests {
		fakekubeclientset := &fakeclientset.Clientset{}
		fakefledgedclientset := &kubefledgedclientsetfake.Clientset{}
		if test.jobListError != nil {
			listError := apierrors.NewInternalError(test.jobListError)
			fakekubeclientset.AddReactor("list", "jobs", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, listError
			})
		} else {
			fakekubeclientset.AddReactor("list", "jobs", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, test.jobList, nil
			})
		}
		if test.jobDeleteError != nil {
			deleteError := apierrors.NewInternalError(test.jobDeleteError)
			fakekubeclientset.AddReactor("delete", "jobs", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, deleteError
			})
		} else {
			fakekubeclientset.AddReactor("delete", "jobs", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, nil
			})
		}

		if test.imageCacheListError != nil {
			listError := apierrors.NewInternalError(test.imageCacheListError)
			fakefledgedclientset.AddReactor("list", "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, listError
			})
		} else {
			fakefledgedclientset.AddReactor("list", "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, test.imageCacheList, nil
			})
		}
		if test.imageCacheUpdateError != nil {
			updateError := apierrors.NewInternalError(test.imageCacheUpdateError)
			fakefledgedclientset.AddReactor("update", "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, updateError
			})
		} else {
			fakefledgedclientset.AddReactor("update", "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, nil, nil
			})
		}

		controller, _, _ := newTestController(fakekubeclientset, fakefledgedclientset)

		err := controller.PreFlightChecks()
		if test.expectErr {
			if !(err != nil && strings.HasPrefix(err.Error(), test.errorString)) {
				t.Errorf("Test: %s failed", test.name)
			}
		} else {
			if err != nil {
				t.Errorf("Test: %s failed. err received = %s", test.name, err.Error())
			}
		}
	}
	t.Logf("%d tests passed", len(tests))
}

func TestRunRefreshWorker(t *testing.T) {
	tests := []struct {
		name                string
		imageCacheList      *kubefledgedv1alpha3.ImageCacheList
		imageCacheListError error
		workqueueItems      int
	}{
		{
			name: "#1: Do not refresh if status is not yet updated",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      0,
		},
		{
			name: "#2: Do not refresh if image cache is already under processing",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      0,
		},
		{
			name: "#3: Do not refresh image cache if cache spec validation failed",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusFailed,
							Reason: kubefledgedv1alpha3.ImageCacheReasonCacheSpecValidationFailed,
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      0,
		},
		{
			name: "#4: Do not refresh if image cache has been purged",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Reason: kubefledgedv1alpha3.ImageCacheReasonImageCachePurge,
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      0,
		},
		{
			name: "#5: Successfully queued 1 imagecache for refresh",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      1,
		},
		{
			name: "#6: Successfully queued 2 imagecaches for refresh",
			imageCacheList: &kubefledgedv1alpha3.ImageCacheList{
				Items: []kubefledgedv1alpha3.ImageCache{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "foo",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "bar",
							Namespace: "kube-fledged",
						},
						Status: kubefledgedv1alpha3.ImageCacheStatus{
							Status: kubefledgedv1alpha3.ImageCacheActionStatusFailed,
						},
					},
				},
			},
			imageCacheListError: nil,
			workqueueItems:      2,
		},
		{
			name:                "#7: No imagecaches to refresh",
			imageCacheList:      nil,
			imageCacheListError: nil,
			workqueueItems:      0,
		},
	}

	for _, test := range tests {
		if test.workqueueItems > 0 {
			//TODO: How to check if workqueue contains the added item?
			continue
		}
		fakekubeclientset := &fakeclientset.Clientset{}
		fakefledgedclientset := &kubefledgedclientsetfake.Clientset{}

		controller, _, imagecacheInformer := newTestController(fakekubeclientset, fakefledgedclientset)
		if test.imageCacheList != nil && len(test.imageCacheList.Items) > 0 {
			for _, imagecache := range test.imageCacheList.Items {
				imagecacheInformer.Informer().GetIndexer().Add(&imagecache)
			}
		}
		controller.runRefreshWorker()
		if test.workqueueItems == controller.workqueue.Len() {
		} else {
			t.Errorf("Test: %s failed: expected %d, actual %d", test.name, test.workqueueItems, controller.workqueue.Len())
		}
	}
}

func TestSyncHandler(t *testing.T) {
	type ActionReaction struct {
		action   string
		reaction string
	}
	now := metav1.Now()
	defaultImageCache := kubefledgedv1alpha3.ImageCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "kube-fledged",
		},
		Spec: kubefledgedv1alpha3.ImageCacheSpec{
			CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
				{
					Images: []kubefledgedv1alpha3.Image{
						{
							Name:           "foo",
							ForceFullCache: false,
						},
					},
				},
			},
		},
	}
	defaultNodeList := &corev1.NodeList{
		Items: []corev1.Node{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "fakenode",
					Labels: map[string]string{"kubernetes.io/hostname": "bar"},
				},
			},
		},
	}

	tests := []struct {
		name              string
		imageCache        kubefledgedv1alpha3.ImageCache
		wqKey             images.WorkQueueKey
		nodeList          *corev1.NodeList
		expectedActions   []ActionReaction
		expectErr         bool
		expectedErrString string
	}{
		{
			name: "#1: Invalid imagecache resource key",
			wqKey: images.WorkQueueKey{
				ObjKey: "foo/bar/car",
			},
			expectErr:         true,
			expectedErrString: "unexpected key format",
		},
		/*{
			name: "#2: Create - Invalid imagecache spec (no images specified)",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{},
						},
					},
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheCreate,
			},
			expectedActions:   []ActionReaction{{action: "update", reaction: ""}},
			expectErr:         true,
			expectedErrString: "No images specified within image list",
		},*/
		{
			name:       "#3: Update - Old imagecache pointer is nil",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:        "kube-fledged/foo",
				WorkType:      images.ImageCacheUpdate,
				OldImageCache: nil,
			},
			nodeList:          defaultNodeList,
			expectedActions:   []ActionReaction{{action: "update", reaction: ""}},
			expectErr:         true,
			expectedErrString: "OldImageCacheNotFound",
		},
		/*{
			name:       "#4: Update - No. of imagelists not equal",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheUpdate,
				OldImageCache: &kubefledgedv1alpha3.ImageCache{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "kube-fledged",
					},
					Spec: kubefledgedv1alpha3.ImageCacheSpec{
						CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
							{
								Images: []kubefledgedv1alpha3.Image{
									kubefledgedv1alpha3.Image{
										Name:            "foo",
										ForceFullCache: true,
									},
								},
							},
							{
								Images: []kubefledgedv1alpha3.Image{
									kubefledgedv1alpha3.Image{
										Name:            "bar",
										ForceFullCache: true,
									},
								},
							},
						},
					},
				},
			},
			nodeList:          defaultNodeList,
			expectedActions:   []ActionReaction{{action: "update", reaction: ""}},
			expectErr:         true,
			expectedErrString: "CacheSpecValidationFailed",
		},
		{
			name:       "#5: Update - Change in NodeSelectors",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheUpdate,
				OldImageCache: &kubefledgedv1alpha3.ImageCache{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "kube-fledged",
					},
					Spec: kubefledgedv1alpha3.ImageCacheSpec{
						CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
							{
								Images: []kubefledgedv1alpha3.Image{
									kubefledgedv1alpha3.Image{
										Name:            "foo",
										ForceFullCache: true,
									},
								},
							},,
								NodeSelector: map[string]string{"foo": "bar"},
							},
						},
					},
				},
			},
			nodeList:          defaultNodeList,
			expectedActions:   []ActionReaction{{action: "update", reaction: ""}},
			expectErr:         true,
			expectedErrString: "CacheSpecValidationFailed",
		},*/
		{
			name:       "#6: Refresh - Update status to processing",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheRefresh,
			},
			nodeList: defaultNodeList,
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: "fake error"},
			},
			expectErr:         true,
			expectedErrString: "Internal error occurred: fake error",
		},
		{
			name: "#7: StatusUpdate - Successful Refresh",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					StartTime: &now,
					Status:    kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
					Reason:    kubefledgedv1alpha3.ImageCacheReasonImageCacheRefresh,
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusSucceeded,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCacheRefresh,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name:       "#8: Purge - Update status to processing",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCachePurge,
			},
			nodeList: defaultNodeList,
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: "fake error"},
			},
			expectErr:         true,
			expectedErrString: "Internal error occurred: fake error",
		},
		{
			name: "#9: Purge - Successful purge",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					Reason: kubefledgedv1alpha3.ImageCacheReasonImageCachePurge,
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheRefresh,
			},
			nodeList: defaultNodeList,
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name:       "#10: Create - Successfully firing imagepull requests",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheCreate,
			},
			nodeList: defaultNodeList,
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name:       "#11: Update - Successfully firing imagepull & imagedelete requests",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheUpdate,
				OldImageCache: &kubefledgedv1alpha3.ImageCache{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "kube-fledged",
					},
					Spec: kubefledgedv1alpha3.ImageCacheSpec{
						CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
							{
								Images: []kubefledgedv1alpha3.Image{
									{
										Name:           "foo",
										ForceFullCache: true,
									},
									{
										Name:           "bar",
										ForceFullCache: false,
									},
								},
							},
						},
					},
				},
			},
			nodeList: defaultNodeList,
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name: "#12: StatusUpdate - ImagesPulledSuccessfully",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					StartTime: &now,
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusSucceeded,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCacheCreate,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name: "#13: StatusUpdate - ImagesDeletedSuccessfully",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					StartTime: &now,
					Status:    kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
					Reason:    kubefledgedv1alpha3.ImageCacheReasonImageCachePurge,
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusSucceeded,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCachePurge,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name:       "#14: StatusUpdate - ImagePullFailedForSomeImages",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusFailed,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCacheCreate,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name:       "#15: StatusUpdate - ImageDeleteFailedForSomeImages",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusFailed,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCachePurge,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
	}

	for _, test := range tests {
		fakekubeclientset := &fakeclientset.Clientset{}
		fakefledgedclientset := &kubefledgedclientsetfake.Clientset{}
		for _, ar := range test.expectedActions {
			if ar.reaction != "" {
				apiError := apierrors.NewInternalError(fmt.Errorf(ar.reaction))
				fakefledgedclientset.AddReactor(ar.action, "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apiError
				})
			}
			fakefledgedclientset.AddReactor(ar.action, "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, &test.imageCache, nil
			})
		}

		controller, nodeInformer, imagecacheInformer := newTestController(fakekubeclientset, fakefledgedclientset)
		if test.nodeList != nil && len(test.nodeList.Items) > 0 {
			for _, node := range test.nodeList.Items {
				nodeInformer.Informer().GetIndexer().Add(&node)
			}
		}
		imagecacheInformer.Informer().GetIndexer().Add(&test.imageCache)
		err := controller.syncHandler(test.wqKey)
		if test.expectErr {
			if err == nil {
				t.Errorf("Test: %s failed: expectedError=%s, actualError=nil", test.name, test.expectedErrString)
			}
			if err != nil && !strings.HasPrefix(err.Error(), test.expectedErrString) {
				t.Errorf("Test: %s failed: expectedError=%s, actualError=%s", test.name, test.expectedErrString, err.Error())
			}
		} else if err != nil {
			t.Errorf("Test: %s failed. expectedError=nil, actualError=%s", test.name, err.Error())
		}
	}
	t.Logf("%d tests passed", len(tests))
}

func TestEnqueueImageCache(t *testing.T) {
	//now := metav1.Now()
	//nowplus5s := metav1.NewTime(time.Now().Add(time.Second * 5))
	defaultImageCache := kubefledgedv1alpha3.ImageCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "kube-fledged",
		},
		Spec: kubefledgedv1alpha3.ImageCacheSpec{
			CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
				{
					Images: []kubefledgedv1alpha3.Image{
						{
							Name:           "foo",
							ForceFullCache: true,
						},
					},
				},
			},
		},
	}
	tests := []struct {
		name           string
		workType       images.WorkType
		oldImageCache  kubefledgedv1alpha3.ImageCache
		newImageCache  kubefledgedv1alpha3.ImageCache
		expectedResult bool
	}{
		{
			name:           "#1: Create - Imagecache queued successfully",
			workType:       images.ImageCacheCreate,
			newImageCache:  defaultImageCache,
			expectedResult: true,
		},
		{
			name:     "#2: Create - Imagecache with Status field, so no queueing",
			workType: images.ImageCacheCreate,
			newImageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
				},
			},
			expectedResult: false,
		},
		{
			name:          "#3: Update - Imagecache purge. Successful queueing",
			workType:      images.ImageCacheUpdate,
			oldImageCache: defaultImageCache,
			newImageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "foo",
					Namespace:   "kube-fledged",
					Annotations: map[string]string{imageCachePurgeAnnotationKey: ""},
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
				},
			},
			expectedResult: true,
		},
		{
			name:           "#4: Update - No change in Spec. Unsuccessful queueing",
			workType:       images.ImageCacheUpdate,
			oldImageCache:  defaultImageCache,
			newImageCache:  defaultImageCache,
			expectedResult: false,
		},
		{
			name:     "#5: Update - Status processing. Unsuccessful queueing",
			workType: images.ImageCacheUpdate,
			oldImageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					Status: kubefledgedv1alpha3.ImageCacheActionStatusProcessing,
				},
			},
			expectedResult: false,
		},
		{
			name:          "#6: Update - Successful queueing",
			workType:      images.ImageCacheUpdate,
			oldImageCache: defaultImageCache,
			newImageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
								{
									Name:           "bar",
									ForceFullCache: false,
								},
							},
						},
					},
				},
			},
			expectedResult: true,
		},
		{
			name:           "#7: Delete - Unsuccessful queueing",
			workType:       images.ImageCacheDelete,
			expectedResult: false,
		},
		{
			name:           "#8: Refresh - Successful queueing",
			workType:       images.ImageCacheRefresh,
			oldImageCache:  defaultImageCache,
			expectedResult: true,
		},
		/*
			{
				name:          "#9: Update - CacheSpec restoration",
				workType:      images.ImageCacheUpdate,
				oldImageCache: defaultImageCache,
				newImageCache: kubefledgedv1alpha3.ImageCache{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "foo",
						Namespace: "kube-fledged",
						Annotations: map[string]string{
							fledgedCacheSpecValidationKey: "failed",
						},
					},
					Spec: kubefledgedv1alpha3.ImageCacheSpec{
						CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
							{
								Images: []kubefledgedv1alpha3.Image{
									kubefledgedv1alpha3.Image{
										Name:            "foo",
										ForceFullCache: true,
									},
									kubefledgedv1alpha3.Image{
										Name:            "bar",
										ForceFullCache: true,
									},
								},
							},
						},
					},
				},
				expectedResult: false,
			},
		*/
		{
			name:          "#10: Update - Imagecache refresh. Successful queueing",
			workType:      images.ImageCacheUpdate,
			oldImageCache: defaultImageCache,
			newImageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "foo",
					Namespace:   "kube-fledged",
					Annotations: map[string]string{imageCacheRefreshAnnotationKey: ""},
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{
								{
									Name:           "foo",
									ForceFullCache: true,
								},
							},
						},
					},
				},
				Status: kubefledgedv1alpha3.ImageCacheStatus{
					Status: kubefledgedv1alpha3.ImageCacheActionStatusSucceeded,
				},
			},
			expectedResult: true,
		},
	}

	for _, test := range tests {
		fakekubeclientset := &fakeclientset.Clientset{}
		fakefledgedclientset := &kubefledgedclientsetfake.Clientset{}
		controller, _, _ := newTestController(fakekubeclientset, fakefledgedclientset)
		result := controller.enqueueImageCache(test.workType, &test.oldImageCache, &test.newImageCache)
		if result != test.expectedResult {
			t.Errorf("Test %s failed: expected=%t, actual=%t", test.name, test.expectedResult, result)
		}
	}
}

func TestProcessNextWorkItem(t *testing.T) {
	type ActionReaction struct {
		action   string
		reaction string
	}
	defaultImageCache := kubefledgedv1alpha3.ImageCache{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "kube-fledged",
		},
		Spec: kubefledgedv1alpha3.ImageCacheSpec{
			CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
				{
					Images: []kubefledgedv1alpha3.Image{
						{
							Name:           "foo",
							ForceFullCache: true,
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name              string
		imageCache        kubefledgedv1alpha3.ImageCache
		wqKey             images.WorkQueueKey
		expectedActions   []ActionReaction
		expectErr         bool
		expectedErrString string
	}{
		{
			name:       "#1: StatusUpdate - ImageDeleteFailedForSomeImages",
			imageCache: defaultImageCache,
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheStatusUpdate,
				Status: &map[string]images.ImageWorkResult{
					"job1": {
						Status: images.ImageWorkResultStatusFailed,
						ImageWorkRequest: images.ImageWorkRequest{
							WorkType: images.ImageCachePurge,
							Node:     &node,
						},
					},
				},
			},
			expectedActions: []ActionReaction{
				{action: "get", reaction: ""},
				{action: "update", reaction: ""},
			},
			expectErr:         false,
			expectedErrString: "",
		},
		{
			name: "#2: Create - Invalid imagecache spec (no images specified)",
			imageCache: kubefledgedv1alpha3.ImageCache{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "kube-fledged",
				},
				Spec: kubefledgedv1alpha3.ImageCacheSpec{
					CacheSpec: []kubefledgedv1alpha3.CacheSpecImages{
						{
							Images: []kubefledgedv1alpha3.Image{},
						},
					},
				},
			},
			wqKey: images.WorkQueueKey{
				ObjKey:   "kube-fledged/foo",
				WorkType: images.ImageCacheCreate,
			},
			expectedActions:   []ActionReaction{{action: "update", reaction: ""}},
			expectErr:         false,
			expectedErrString: "No images specified within image list",
		},
		{
			name:              "#3: Unexpected type in workqueue",
			expectErr:         false,
			expectedErrString: "Unexpected type in workqueue",
		},
	}

	for _, test := range tests {
		fakekubeclientset := &fakeclientset.Clientset{}
		fakefledgedclientset := &kubefledgedclientsetfake.Clientset{}
		for _, ar := range test.expectedActions {
			if ar.reaction != "" {
				apiError := apierrors.NewInternalError(fmt.Errorf(ar.reaction))
				fakefledgedclientset.AddReactor(ar.action, "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
					return true, nil, apiError
				})
			}
			fakefledgedclientset.AddReactor(ar.action, "imagecaches", func(action core.Action) (handled bool, ret runtime.Object, err error) {
				return true, &test.imageCache, nil
			})
		}

		controller, _, imagecacheInformer := newTestController(fakekubeclientset, fakefledgedclientset)
		imagecacheInformer.Informer().GetIndexer().Add(&test.imageCache)
		if test.expectedErrString == "Unexpected type in workqueue" {
			controller.workqueue.Add(struct{}{})
		}
		controller.workqueue.Add(test.wqKey)
		controller.processNextWorkItem()
		var err error
		if test.expectErr {
			if err == nil {
				t.Errorf("Test: %s failed: expectedError=%s, actualError=nil", test.name, test.expectedErrString)
			}
			if err != nil && !strings.HasPrefix(err.Error(), test.expectedErrString) {
				t.Errorf("Test: %s failed: expectedError=%s, actualError=%s", test.name, test.expectedErrString, err.Error())
			}
		} else if err != nil {
			t.Errorf("Test: %s failed. expectedError=nil, actualError=%s", test.name, err.Error())
		}
	}
	t.Logf("%d tests passed", len(tests))
}
