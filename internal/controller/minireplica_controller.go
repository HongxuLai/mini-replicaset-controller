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

	// 1. get MiniReplica
	instance := &appsv1.MiniReplica{} // create a empty variable to record this mmoment
	// use Get() to get exactly a MiniReplica object
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		// instance will get real content of Minireplica, like Name, Namespace, UID...
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// 2. list candidate pods.
	pods, err := r.listCandidatePods(ctx, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// 3. filter owned pods
	ownedPods, err := r.claimPods(ctx, instance, pods)
	if err != nil {
		return ctrl.Result{}, err
	}

	// finish the judgement of which pod belongs to now MiniReplica
	// begin to manage replicas

	// 4. compare desired and actual, then create / delete
	if err := r.reconcileReplicas(ctx, req.Namespace, instance, ownedPods); err != nil {
		return ctrl.Result{}, err
	}

	// 5. update status 	
	instance.Status.Running = int32(len(ownedPods)) // cast: Running's type is int32
	if err := r.Status().Update(ctx, instance); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("reconcile finished",
		"miniReplica", instance.Name,
		"desired", instance.Spec.Replicas,
		"actual", len(ownedPods),
	)

	return ctrl.Result{}, nil
}

// a helper used to list all candidate pods
func (r *MiniReplicaReconciler) listCandidatePods(ctx context.Context, namespace string) ([]corev1.Pod, error) {
	// go through every pod, use controller's selector to match pod's labels
	podList := &corev1.PodList{}
	if err := r.List( // similar to Get, get a list of elements
		ctx,
		podList,
		client.InNamespace(namespace),
		// list the pod with in this namespace
	); err != nil {
		return nil, err
	}
	return podList.Items, nil
}

// a helper used to filter pods
func (r *MiniReplicaReconciler) claimPods(
	ctx context.Context,
	instance *appsv1.MiniReplica,
	pods []corev1.Pod,
) ([]*corev1.Pod, error) {
	log := logf.FromContext(ctx)

	var ownedPods []*corev1.Pod

	for i := range pods {
		pod := &pods[i]
		owner := metav1.GetControllerOf(pod)

		// create a simple selector
		match := pod.Labels["minireplica"] == instance.Name

		// case1: an orphan pod, begin to adopt
		if owner == nil {
			if !match {
				continue
			}
			log.Info("adopting orphan pod", "podName", pod.Name)

			// set the pod's ownerRef to current MiniReplica, metadata is changed
			if err := controllerutil.SetControllerReference(instance, pod, r.Scheme); err != nil {
				return nil, err
			}

			// update this changed Pod to API server
			if err := r.Update(ctx, pod); err != nil {
				log.Error(err, "failed to adopt orphan pod", "podName", pod.Name)
				return nil, err
			}

			// add this pod to ownedPods
			ownedPods = append(ownedPods, pod)
			continue
		}

		// case 2: pod is already owned by current MiniReplica
		// ownerRef is matched
		if owner.Kind == "MiniReplica" && owner.Name == instance.Name && owner.UID == instance.UID {
			if match {
				// still matches selector, keep as owned
				ownedPods = append(ownedPods, pod)
			} else {
				// ownerRef is mached, but the labels are not matched
				// logical release: this pod won't be seen as the MiniReplica's pod
				log.Info("logical release pod because selector no longer matches",
					"podName", pod.Name,
					"ownerName", owner.Name,
				)
			}
			continue
		}

		// case3: record the pod owned by another controller
		log.Info("skip pod owned by another controller",
			"podName", pod.Name,
			"ownerKind", owner.Kind,
			"ownerName", owner.Name,
		)
	}
	return ownedPods, nil
}

// a helper used to compare actual and desired, and exactly begin to delete / create pods
func (r *MiniReplicaReconciler) reconcileReplicas(
	ctx context.Context,
	namespace string,
	instance *appsv1.MiniReplica,
	ownedPods []*corev1.Pod,
) error {
	log := logf.FromContext(ctx)

	// calculate actual and desired pod
	actualCount := len(ownedPods)
	desiredCount := int(instance.Spec.Replicas)
	// calculate difference
	diff := desiredCount - actualCount

	// create a selector
	matchLabels := map[string]string{
		"minireplica": instance.Name,
	}

	if diff > 0 {
		log.Info("enter create branch", "count", diff)

		// first construct a pod's information 
		// including including ObjectMeta(who is the Pod) and Spec(what can the Pod do)
		for i := 0; i < diff; i++ {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: instance.Name + "-pod-",
					Namespace:    namespace,
					Labels:       matchLabels,
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

			// set the pod's ownerRef to now MiniReplica object before creating
			if err := controllerutil.SetControllerReference(instance, pod, r.Scheme); err != nil {
				return err
			}

			log.Info("creating pod", "generateName", pod.GenerateName)

			// really create a new pod
			if err := r.Create(ctx, pod); err != nil {
				log.Error(err, "failed to create pod", "generateName", pod.GenerateName)
				return err
			}
		}
	} else if diff < 0 {
		toDelete := -diff
		log.Info("enter delete branch", "count", toDelete)

		// delete some first owned pods
		for i := 0; i < toDelete; i++ {
			pod := ownedPods[i]
			log.Info("deleting pod", "podName", pod.Name)

			if err := r.Delete(ctx, pod); err != nil {
				return err
			}
		}
	}

	return nil
}


// SetupWithManager sets up the controller with the Manager.
func (r *MiniReplicaReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appsv1.MiniReplica{}).
		Owns(&corev1.Pod{}).
		Named("minireplica").
		Complete(r)
}
