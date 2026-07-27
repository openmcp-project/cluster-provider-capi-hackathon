package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	apiconst "github.com/openmcp-project/openmcp-operator/api/constants"

	capiv1alpha1 "github.com/openmcp-project/cluster-provider-capi/api/v1alpha1"
)

type ProviderConfigReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
}

func NewProviderConfigReconciler(platformCluster *clusters.Cluster, providerName string) *ProviderConfigReconciler {
	return &ProviderConfigReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
	}
}

func (r *ProviderConfigReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logf.FromContext(ctx)

	obj := &capiv1alpha1.ProviderConfig{}
	if err := r.platformCluster.Client().Get(ctx, req.NamespacedName, obj); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	if obj.GetAnnotations() != nil {
		op, ok := obj.GetAnnotations()[apiconst.OperationAnnotation]
		if ok {
			switch op {
			case apiconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to operation annotation")
				return reconcile.Result{}, nil
			case apiconst.OperationAnnotationValueReconcile:
				if err := ctrlutils.EnsureAnnotation(ctx, r.platformCluster.Client(), obj, apiconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return reconcile.Result{}, fmt.Errorf("error removing operation annotation: %w", err)
				}
			}
		}
	}

	if err := r.reconcileProviderConfig(ctx, obj); err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *ProviderConfigReconciler) reconcileProviderConfig(ctx context.Context, obj *capiv1alpha1.ProviderConfig) error {
	patch := client.MergeFrom(obj.DeepCopy())

	meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ReconcileSuccess",
		Message:            "ProviderConfig is ready",
		ObservedGeneration: obj.GetGeneration(),
	})
	obj.Status.ObservedGeneration = obj.GetGeneration()
	obj.Status.Phase = "Ready"

	if err := r.platformCluster.Client().Status().Patch(ctx, obj, patch); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("error updating ProviderConfig status: %w", err)
		}
	}
	return nil
}

func (r *ProviderConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&capiv1alpha1.ProviderConfig{}).
		Complete(r)
}
