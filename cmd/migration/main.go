package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/golang/glog"
	"github.com/senthilrch/kube-fledged/pkg/apis/kubefledged/v1alpha2"
	clientset "github.com/senthilrch/kube-fledged/pkg/client/clientset/versioned"
	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	masterURL       string
	kubeConfig      string
	enableFullCache bool
)

func init() {
	flag.StringVar(&kubeConfig, "kubeconfig", "",
		"Path to a kubeconfig. Only required if out-of-cluster.")
	flag.StringVar(&masterURL, "master", "",
		"The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.")
	flag.BoolVar(&enableFullCache, "enableFullCache", false,
		"Whether enable cache all feature of migration imagecaches.")
}

func main() {
	flag.Parse()

	clientCmdConfig, err := clientcmd.BuildConfigFromFlags(masterURL, kubeConfig)
	if err != nil {
		glog.Fatalf("error building kubeconfig: %s", err.Error())
	}

	client, err := clientset.NewForConfig(clientCmdConfig)
	if err != nil {
		glog.Fatalf("error building Inference clientset: %s", err.Error())
	}

	old, err := client.KubefledgedV1alpha2().ImageCaches("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		glog.Fatalf("error listing Inferences: %s", err.Error())
	}

	for _, o := range old.Items {
		spec := []v1alpha2.CacheSpecImages{}
		for _, s := range o.Spec.CacheSpec {
			ForceCacheAll := make([]bool, len(s.Images))
			for i := range ForceCacheAll {
				ForceCacheAll[i] = enableFullCache
			}
			spec = append(spec, v1alpha2.CacheSpecImages{
				Images:        s.Images,
				ForceCacheAll: ForceCacheAll,
				NodeSelector:  s.NodeSelector,
			})
		}
		logrus.Info("Migrating Inference: ", o.Name, o.Namespace)
		Failures := map[string]v1alpha2.NodeReasonMessageList{}
		for k, messageList := range o.Status.Failures {
			l := []v1alpha2.NodeReasonMessage{}
			for _, v := range messageList {
				l = append(l, v1alpha2.NodeReasonMessage(v))
			}
			Failures[k] = l
		}
		new := &v1alpha2.ImageCache{
			TypeMeta:   o.TypeMeta,
			ObjectMeta: o.ObjectMeta,
			Spec: v1alpha2.ImageCacheSpec{
				CacheSpec:        spec,
				ImagePullSecrets: o.Spec.ImagePullSecrets,
			},
			Status: v1alpha2.ImageCacheStatus{
				Status:         v1alpha2.ImageCacheActionStatus(o.Status.Status),
				Reason:         o.Status.Reason,
				Message:        o.Status.Message,
				Failures:       Failures,
				StartTime:      o.Status.StartTime,
				CompletionTime: o.Status.CompletionTime,
			},
		}

		_, err = client.KubefledgedV1alpha2().ImageCaches(o.Namespace).Update(context.TODO(), new, metav1.UpdateOptions{})
		if err != nil {
			glog.Fatalf("error creating Inference: %s", err.Error())
		}
	}

	new, err := client.KubefledgedV1alpha2().ImageCaches("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		glog.Fatalf("error listing Inferences: %s", err.Error())
	}

	for _, n := range new.Items {
		fmt.Printf("%s\n", n.Name)
	}
}
