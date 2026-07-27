package controller

import (
	"context"
	"fmt"

	"hash/fnv"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	errutils "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/controller-utils/pkg/logging"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	clusterconst "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1/constants"
	commonapi "github.com/openmcp-project/openmcp-operator/api/common"
	openmcpconst "github.com/openmcp-project/openmcp-operator/api/constants"

	capiv1alpha1 "github.com/openmcp-project/cluster-provider-capi/api/v1alpha1"
)

type ClusterReconciler struct {
	platformCluster *clusters.Cluster
	providerName    string
}

func NewClusterReconciler(platformCluster *clusters.Cluster, providerName string) *ClusterReconciler {
	return &ClusterReconciler{
		platformCluster: platformCluster,
		providerName:    providerName,
	}
}

type ReconcileResult = ctrlutils.ReconcileResult[*clustersv1alpha1.Cluster]

func (r *ClusterReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := logging.FromContextOrPanic(ctx).WithName("ClusterReconciler")
	ctx = logging.NewContext(ctx, log)
	log.Info("Starting reconcile")

	rr := r.reconcile(ctx, req)

	result, err := ctrlutils.NewOpenMCPStatusUpdaterBuilder[*clustersv1alpha1.Cluster]().
		WithNestedStruct("Status").
		WithPhaseUpdateFunc(func(obj *clustersv1alpha1.Cluster, rr ReconcileResult) (string, error) {
			if rr.Object != nil && !rr.Object.DeletionTimestamp.IsZero() {
				return commonapi.StatusPhaseTerminating, nil
			}
			for _, cond := range obj.Status.Conditions {
				if cond.Status != metav1.ConditionTrue {
					return commonapi.StatusPhaseProgressing, nil
				}
			}
			if len(obj.Status.Conditions) == 0 {
				return commonapi.StatusPhaseProgressing, nil
			}
			return commonapi.StatusPhaseReady, nil
		}).
		WithConditionUpdater(false).
		Build().
		UpdateStatus(ctx, r.platformCluster.Client(), rr)

	return result, err
}

func (r *ClusterReconciler) reconcile(ctx context.Context, req reconcile.Request) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)

	c := &clustersv1alpha1.Cluster{}
	if err := r.platformCluster.Client().Get(ctx, req.NamespacedName, c); err != nil {
		if apierrors.IsNotFound(err) {
			return ReconcileResult{}
		}
		return ReconcileResult{ReconcileError: errutils.WithReason(
			fmt.Errorf("unable to get Cluster '%s': %w", req.String(), err),
			clusterconst.ReasonPlatformClusterInteractionProblem,
		)}
	}

	// handle operation annotation
	if c.GetAnnotations() != nil {
		op, ok := c.GetAnnotations()[openmcpconst.OperationAnnotation]
		if ok {
			switch op {
			case openmcpconst.OperationAnnotationValueIgnore:
				log.Info("Ignoring resource due to operation annotation")
				return ReconcileResult{}
			case openmcpconst.OperationAnnotationValueReconcile:
				if err := ctrlutils.EnsureAnnotation(ctx, r.platformCluster.Client(), c, openmcpconst.OperationAnnotation, "", true, ctrlutils.DELETE); err != nil {
					return ReconcileResult{ReconcileError: errutils.WithReason(
						fmt.Errorf("error removing operation annotation: %w", err),
						clusterconst.ReasonPlatformClusterInteractionProblem,
					)}
				}
			}
		}
	}

	// only handle clusters whose profile this provider owns
	if c.Spec.Profile != ProfileName {
		log.Info("Ignoring cluster: profile not owned by this provider", "profile", c.Spec.Profile)
		return ReconcileResult{}
	}

	rr := ReconcileResult{
		Object:     c,
		OldObject:  c.DeepCopy(),
		Conditions: []metav1.Condition{},
	}

	if !c.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, c, &rr)
	}
	return r.handleCreateOrUpdate(ctx, c, &rr)
}

func (r *ClusterReconciler) handleCreateOrUpdate(ctx context.Context, c *clustersv1alpha1.Cluster, rr *ReconcileResult) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	createCon := ctrlutils.GenerateCreateConditionFunc(rr)

	// ensure our finalizer
	if controllerutil.AddFinalizer(c, ClusterFinalizer) {
		if err := r.platformCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("error adding finalizer: %w", err),
				clusterconst.ReasonPlatformClusterInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
		rr.OldObject = c.DeepCopy()
	}

	// look up the ProviderConfig to get the ClusterClass name
	pc, err := r.getProviderConfig(ctx, c)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(
			fmt.Errorf("error getting ProviderConfig: %w", err),
			ReasonCAPIInteractionProblem,
		)
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return *rr
	}

	capiClusterName := capiClusterName(c)
	capiNamespace := capiClusterNamespace(c, pc)

	existing := &clusterv1.Cluster{}
	err = r.platformCluster.Client().Get(ctx, client.ObjectKey{Name: capiClusterName, Namespace: capiNamespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		rr.ReconcileError = errutils.WithReason(
			fmt.Errorf("error fetching CAPI Cluster '%s/%s': %w", capiNamespace, capiClusterName, err),
			ReasonCAPIInteractionProblem,
		)
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return *rr
	}

	if apierrors.IsNotFound(err) {
		if c.Spec.Kubernetes.Version == "" {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("spec.kubernetes.version is required for CAPI clusters"),
				ReasonCAPIInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
		log.Info("Creating CAPI Cluster", "capiName", capiClusterName, "capiNamespace", capiNamespace)
		desired, buildErr := r.buildCAPICluster(ctx, c, pc, capiClusterName, capiNamespace)
		if buildErr != nil {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("error building CAPI Cluster: %w", buildErr),
				ReasonCAPIInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
		if createErr := r.platformCluster.Client().Create(ctx, desired); createErr != nil {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("error creating CAPI Cluster '%s/%s': %w", capiNamespace, capiClusterName, createErr),
				ReasonCAPIInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, "ClusterProvisioning", "CAPI Cluster created, waiting for it to become ready")
		rr.Result.RequeueAfter = provisioningRequeueInterval
		return *rr
	}

	// cluster exists — reflect its readiness
	ready := isCAPIClusterReady(existing)
	if !ready {
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, "ClusterNotReady", fmt.Sprintf("CAPI Cluster phase: %s", existing.Status.Phase))
		rr.Result.RequeueAfter = provisioningRequeueInterval
		return *rr
	}

	createCon(ConditionCAPIManagement, metav1.ConditionTrue, "ClusterReady", "")

	// expose control-plane endpoint
	if existing.Spec.ControlPlaneEndpoint.Host != "" {
		c.Status.Endpoints = clustersv1alpha1.Endpoints{}
		host := existing.Spec.ControlPlaneEndpoint.Host
		port := existing.Spec.ControlPlaneEndpoint.Port
		if port != 0 {
			c.Status.Endpoints.Set("default", fmt.Sprintf("https://%s:%d", host, port))
		} else {
			c.Status.Endpoints.Set("default", "https://"+host)
		}
	}

	return *rr
}

func (r *ClusterReconciler) handleDelete(ctx context.Context, c *clustersv1alpha1.Cluster, rr *ReconcileResult) ReconcileResult {
	log := logging.FromContextOrPanic(ctx)
	createCon := ctrlutils.GenerateCreateConditionFunc(rr)

	// wait for foreign finalizers
	foreignFinalizers := []string{}
	for _, fin := range c.Finalizers {
		if fin != ClusterFinalizer {
			foreignFinalizers = append(foreignFinalizers, fin)
		}
	}
	if len(foreignFinalizers) > 0 {
		log.Info("Waiting for foreign finalizers", "finalizers", foreignFinalizers)
		createCon(ConditionForeignFinalizers, metav1.ConditionFalse, ReasonWaitingForDeletion,
			fmt.Sprintf("Waiting for foreign finalizers: %v", foreignFinalizers))
		return *rr
	}
	createCon(ConditionForeignFinalizers, metav1.ConditionTrue, "NoForeignFinalizers", "")

	pc, err := r.getProviderConfig(ctx, c)
	if err != nil {
		rr.ReconcileError = errutils.WithReason(
			fmt.Errorf("error getting ProviderConfig: %w", err),
			ReasonCAPIInteractionProblem,
		)
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return *rr
	}

	capiClusterName := capiClusterName(c)
	capiNamespace := capiClusterNamespace(c, pc)

	existing := &clusterv1.Cluster{}
	err = r.platformCluster.Client().Get(ctx, client.ObjectKey{Name: capiClusterName, Namespace: capiNamespace}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		rr.ReconcileError = errutils.WithReason(
			fmt.Errorf("error fetching CAPI Cluster '%s/%s': %w", capiNamespace, capiClusterName, err),
			ReasonCAPIInteractionProblem,
		)
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
		return *rr
	}

	if !apierrors.IsNotFound(err) {
		// already deleting
		if existing.DeletionTimestamp != nil {
			log.Info("CAPI Cluster is already being deleted, waiting")
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, ReasonWaitingForDeletion, "Waiting for CAPI Cluster to be deleted")
			rr.Result.RequeueAfter = provisioningRequeueInterval
			return *rr
		}
		log.Info("Deleting CAPI Cluster", "capiName", capiClusterName, "capiNamespace", capiNamespace)
		if deleteErr := r.platformCluster.Client().Delete(ctx, existing); deleteErr != nil && !apierrors.IsNotFound(deleteErr) {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("error deleting CAPI Cluster '%s/%s': %w", capiNamespace, capiClusterName, deleteErr),
				ReasonCAPIInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
		createCon(ConditionCAPIManagement, metav1.ConditionFalse, ReasonWaitingForDeletion, "CAPI Cluster deletion initiated")
		rr.Result.RequeueAfter = provisioningRequeueInterval
		return *rr
	}

	createCon(ConditionCAPIManagement, metav1.ConditionTrue, "ClusterDeleted", "CAPI Cluster no longer exists")

	if controllerutil.RemoveFinalizer(c, ClusterFinalizer) {
		if err := r.platformCluster.Client().Patch(ctx, c, client.MergeFrom(rr.OldObject)); err != nil {
			rr.ReconcileError = errutils.WithReason(
				fmt.Errorf("error removing finalizer: %w", err),
				clusterconst.ReasonPlatformClusterInteractionProblem,
			)
			createCon(ConditionCAPIManagement, metav1.ConditionFalse, rr.ReconcileError.Reason(), rr.ReconcileError.Error())
			return *rr
		}
	}
	rr.Object = nil
	return *rr
}

func (r *ClusterReconciler) getProviderConfig(ctx context.Context, _ *clustersv1alpha1.Cluster) (*capiv1alpha1.ProviderConfig, error) {
	pcList := &capiv1alpha1.ProviderConfigList{}
	if err := r.platformCluster.Client().List(ctx, pcList); err != nil {
		return nil, err
	}
	for i := range pcList.Items {
		if pcList.Items[i].Spec.ProviderRef.Name == r.providerName {
			return &pcList.Items[i], nil
		}
	}
	return nil, fmt.Errorf("no ProviderConfig found for provider %q", r.providerName)
}

func (r *ClusterReconciler) buildCAPICluster(
	ctx context.Context,
	c *clustersv1alpha1.Cluster,
	pc *capiv1alpha1.ProviderConfig,
	name, namespace string,
) (*clusterv1.Cluster, error) {
	classNamespace := pc.Spec.ClusterClassNamespace
	if classNamespace == "" {
		classNamespace = namespace
	}
	version := c.Spec.Kubernetes.Version
	if len(version) > 0 && version[0] != 'v' {
		version = "v" + version
	}

	vars := make([]clusterv1.ClusterVariable, 0, len(pc.Spec.TopologyVariables))
	for _, v := range pc.Spec.TopologyVariables {
		vars = append(vars, clusterv1.ClusterVariable{
			Name:  v.Name,
			Value: apiextensionsv1.JSON{Raw: v.Value.Raw},
		})
	}

	// look up machine pool classes defined in the ClusterClass and include them all
	cc := &clusterv1.ClusterClass{}
	ccNamespace := pc.Spec.ClusterClassNamespace
	if ccNamespace == "" {
		ccNamespace = namespace
	}
	if err := r.platformCluster.Client().Get(ctx, client.ObjectKey{Name: pc.Spec.ClusterClassName, Namespace: ccNamespace}, cc); err != nil {
		return nil, fmt.Errorf("error fetching ClusterClass '%s/%s': %w", ccNamespace, pc.Spec.ClusterClassName, err)
	}
	var pools []clusterv1.MachinePoolTopology
	if len(cc.Spec.Workers.MachinePools) > 0 {
		replicas := int32(3)
		for _, mp := range cc.Spec.Workers.MachinePools {
			pools = append(pools, clusterv1.MachinePoolTopology{
				Class:    mp.Class,
				Name:     mp.Class,
				Replicas: &replicas,
			})
		}
	}

	topology := &clusterv1.Topology{
		Class:     pc.Spec.ClusterClassName,
		Version:   version,
		Variables: vars,
	}
	if len(pools) > 0 {
		topology.Workers = &clusterv1.WorkersTopology{
			MachinePools: pools,
		}
	}

	return &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: clusterv1.ClusterSpec{
			Topology: topology,
		},
	}, nil
}

func isCAPIClusterReady(c *clusterv1.Cluster) bool {
	return c.Status.Phase == string(clusterv1.ClusterPhaseProvisioned) &&
		c.Status.InfrastructureReady &&
		c.Status.ControlPlaneReady
}

// capiClusterName derives a short deterministic CAPI Cluster name from the OpenControlPlane Cluster.
// We hash namespace+name to stay well under GKE's 40-char node pool name limit.
func capiClusterName(c *clustersv1alpha1.Cluster) string {
	h := fnv.New32a()
	_, _ = fmt.Fprintf(h, "%s/%s", c.Namespace, c.Name)
	return fmt.Sprintf("capi-%08x", h.Sum32())
}

// capiClusterNamespace returns the namespace for the CAPI Cluster.
// It uses the ClusterClassNamespace from the ProviderConfig so that the Cluster
// lives in the same namespace as its ClusterClass (CAPI requirement).
// Falls back to the OpenControlPlane Cluster's own namespace if not configured.
func capiClusterNamespace(c *clustersv1alpha1.Cluster, pc *capiv1alpha1.ProviderConfig) string {
	if pc.Spec.ClusterClassNamespace != "" {
		return pc.Spec.ClusterClassNamespace
	}
	if c.Namespace == "" {
		return "default"
	}
	return c.Namespace
}

func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clustersv1alpha1.Cluster{}).
		WithEventFilter(predicate.And(
			predicate.Or(
				predicate.GenerationChangedPredicate{},
				ctrlutils.DeletionTimestampChangedPredicate{},
				ctrlutils.GotAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueReconcile),
				ctrlutils.LostAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
			),
			predicate.Not(
				ctrlutils.HasAnnotationPredicate(openmcpconst.OperationAnnotation, openmcpconst.OperationAnnotationValueIgnore),
			),
		)).
		Watches(
			&clusterv1.Cluster{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromCAPICluster),
			builder.WithPredicates(predicate.Or(
				predicate.GenerationChangedPredicate{},
				ctrlutils.DeletionTimestampChangedPredicate{},
			)),
		).
		Watches(
			&clustersv1alpha1.ClusterProfile{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				if obj == nil {
					return nil
				}
				clusterList := &clustersv1alpha1.ClusterList{}
				if err := r.platformCluster.Client().List(ctx, clusterList, client.MatchingFields{
					"spec.profile": obj.GetName(),
				}); err != nil {
					return nil
				}
				reqs := make([]reconcile.Request, len(clusterList.Items))
				for i, cl := range clusterList.Items {
					reqs[i] = reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&cl)}
				}
				return reqs
			}),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}),
		).
		Complete(r)
}

// enqueueFromCAPICluster maps a CAPI Cluster event back to the owning OpenControlPlane Cluster.
func (r *ClusterReconciler) enqueueFromCAPICluster(ctx context.Context, obj client.Object) []reconcile.Request {
	capiCluster, ok := obj.(*clusterv1.Cluster)
	if !ok {
		return nil
	}
	// CAPI cluster name is "<openmcp-namespace>-<openmcp-name>"
	// namespace equals capiCluster.Namespace
	clusterList := &clustersv1alpha1.ClusterList{}
	if err := r.platformCluster.Client().List(ctx, clusterList,
		client.InNamespace(capiCluster.Namespace),
		client.MatchingFields{"spec.profile": ProfileName},
	); err != nil {
		return nil
	}
	reqs := []reconcile.Request{}
	for i := range clusterList.Items {
		cl := &clusterList.Items[i]
		if capiClusterName(cl) == capiCluster.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cl)})
		}
	}
	return reqs
}
