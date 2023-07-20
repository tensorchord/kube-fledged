// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package images

import (
	"fmt"
	"strings"
	"time"

	fledgedv1alpha2 "github.com/senthilrch/kube-fledged/pkg/apis/kubefledged/v1alpha2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// common Job will cache all at default status, but none at streaming mode of GCP
func commonJob(imagecache *fledgedv1alpha2.ImageCache, image string, pullPolicy corev1.PullPolicy,
	hostname string, labels map[string]string, busyboxImage string) *batchv1.Job {
	backoffLimit := int32(0)
	activeDeadlineSeconds := int64((time.Hour).Seconds())

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: imagecache.Name + "-",
			Namespace:    imagecache.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(imagecache, schema.GroupVersionKind{
					Group:   fledgedv1alpha2.SchemeGroupVersion.Group,
					Version: fledgedv1alpha2.SchemeGroupVersion.Version,
					Kind:    "ImageCache",
				}),
			},
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: imagecache.Namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": hostname,
					},
					InitContainers: []corev1.Container{
						{
							Name:    "busybox",
							Image:   busyboxImage,
							Command: []string{"cp", "/bin/echo", "/tmp/bin"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tmp-bin",
									MountPath: "/tmp/bin",
								},
							},
							ImagePullPolicy: corev1.PullIfNotPresent,
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "imagepuller",
							Image:   image,
							Command: []string{"/tmp/bin/echo", "Image pulled successfully!"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "tmp-bin",
									MountPath: "/tmp/bin",
								},
							},
							ImagePullPolicy: pullPolicy,
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "tmp-bin",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					RestartPolicy:    corev1.RestartPolicyNever,
					ImagePullSecrets: imagecache.Spec.ImagePullSecrets,
					Tolerations: []corev1.Toleration{
						{
							Operator: corev1.TolerationOpExists,
						},
					},
				},
			},
		},
	}
}

// special Job to cache common used files and directories at streaming mode of GCP
func dirCacheJob(imagecache *fledgedv1alpha2.ImageCache, image string, pullPolicy corev1.PullPolicy,
	hostname string, labels map[string]string, cacheDir []string) *batchv1.Job {
	backoffLimit := int32(0)
	activeDeadlineSeconds := int64((time.Hour).Seconds())

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: imagecache.Name + "-",
			Namespace:    imagecache.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(imagecache, schema.GroupVersionKind{
					Group:   fledgedv1alpha2.SchemeGroupVersion.Group,
					Version: fledgedv1alpha2.SchemeGroupVersion.Version,
					Kind:    "ImageCache",
				}),
			},
			Labels: labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadlineSeconds,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: imagecache.Namespace,
					Labels:    labels,
				},
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"kubernetes.io/hostname": hostname,
					},
					Containers: []corev1.Container{
						{
							Name:  "imagepuller",
							Image: image,
							Command: []string{
								"bash",
								"-c",
								fmt.Sprintf("find %s "+
									"-prune -o -path \"/dev/*\" "+
									"-prune -o -path \"/proc/*\" "+
									"-prune -o -path \"/sys/*\" "+
									"-prune -o -path \"/mnt/*\" "+
									"-type f -print0 | xargs -0 cat > /dev/null || true",
									strings.Join(cacheDir, " ")),
							},
							ImagePullPolicy: pullPolicy,
						},
					},
					RestartPolicy:    corev1.RestartPolicyNever,
					ImagePullSecrets: imagecache.Spec.ImagePullSecrets,
					Tolerations: []corev1.Toleration{
						{
							Operator: corev1.TolerationOpExists,
						},
					},
				},
			},
		},
	}
}

// special Job to cache all files used at streaming mode of GCP
func fullCacheJob(imagecache *fledgedv1alpha2.ImageCache, image string, pullPolicy corev1.PullPolicy,
	hostname string, labels map[string]string) *batchv1.Job {
	return dirCacheJob(imagecache, image, pullPolicy, hostname, labels, []string{"/"})
}
