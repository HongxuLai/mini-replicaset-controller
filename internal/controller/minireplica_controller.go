/*
Copyright 2026.

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

package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	appsv1 "github.com/HongxuLai/mini-replicaset-controller/api/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// MiniReplicaReconciler reconciles a MiniReplica object
type MiniReplicaReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps.example.com,resources=minireplicas,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.example.com,resources=minireplicas/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.example.com,resources=minireplicas/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the MiniReplica object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.1/pkg/reconcile
func (r *MiniReplicaReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	// code under is used to debugging
	// log.Info("reconcile triggered", "name", req.Name, "namespace", req.Namespace)
	// get the object and judge if that's can be found
	instance := &appsv1.MiniReplica{} // variable to record this mmoment
	// use Get() to get exactly a MiniReplica object
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// use labels to judge if this Pod belongs to the object
	matchlabels := map[string]string{
		"minireplica": instance.Name,
	} // create key-value map

	podList := &corev1.PodList{}
	if err := r.List( // similar to Get, get a list of elements
		ctx,
		podList,
		client.InNamespace(req.Namespace), // retrict the range   
	); err != nil {
		return ctrl.Result{}, err
	}

	var ownedPods []*corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]

		// check label match
		if pod.Labels["minireplica"] != instance.Name{
			continue
		}

		// check the pod's current controller
		owner := metav1.GetControllerOf(pod)

		// case1: an orphan pod
		if owner == nil { 
			log.Info("adopting orphan pod", "podName", pod.Name)

			// change the pod's ownerRef to current MiniReplica
			if err := controllerutil.SetControllerReference(instance, pod, r.Scheme); err != nil{
				return ctrl.Result{}, err
			}

			// update this changed Pod to API server
			if err := r.Update(ctx, pod); err != nil {
				log.Error(err, "failed to adopt orphan pod", "podName", pod.Name)
				return ctrl.Result{}, err
			}

			ownedPods = append(ownedPods, pod)
			continue
		}

		// case2: this pod already owned by the MiniReplica object
		if owner.Kind == "MiniReplica" && owner.Name == instance.Name && owner.UID == instance.UID {
			ownedPods = append(ownedPods, pod)
			continue
		}
	
		// record the pod owned by another controller
		log.Info("skip pod owned by another controller",
			"podName", pod.Name,
			"ownerKind", owner.Kind,
			"ownerName", owner.Name,
		)

		continue
	}

	actualCount := len(ownedPods)
	desiredCount := int(instance.Spec.Replicas)
	diff := desiredCount - actualCount // calculate the difference between desired and actual
	/* used to debugging
	log.Info("reconcile state",
	"desired", desiredCount,
	"actual", actualCount,
	"diff", diff,
	)
	for _, p := range podList.Items {
	log.Info("matched pod",
		"podName", p.Name,
		"phase", p.Status.Phase,
		"namespace", p.Namespace,
	)
	}
	*/
	
	if diff > 0 {
		log.Info("enter create branch", "count", diff)
		for i := 0; i < diff; i++ {
			// construct the pod, including ObjectMeta(who is the Pod) and Spec(what can the Pod do)
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: instance.Name + "-pod-",
					Namespace:    req.Namespace,
					Labels:       matchlabels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx:latest",
						},
					},
				},
			}
			if err := controllerutil.SetControllerReference(instance, pod, r.Scheme); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("creating pod", "generateName", pod.GenerateName)
			if err := r.Create(ctx, pod); err != nil {
				log.Error(err, "failed to create pod", "generateName", pod.GenerateName)
				return ctrl.Result{}, err
			}
		}
	} else if diff < 0 {
		toDelete := -diff
		for i := 0; i < toDelete; i++ {
			pod := &podList.Items[i]
			if err := r.Delete(ctx, pod); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	instance.Status.Running = int32(actualCount) // cast: Running's type is int32
	if err := r.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MiniReplicaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.MiniReplica{}).
		Owns(&corev1.Pod{}).
		Named("minireplica").
		Complete(r)
}
