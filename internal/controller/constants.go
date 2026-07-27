package controller

import "time"

const (
	// ProfileName is the ClusterProfile name this provider handles.
	ProfileName = "capi"

	// ClusterFinalizer is placed on Cluster resources managed by this provider.
	ClusterFinalizer = "capi.cluster.open-control-plane.io/finalizer"

	// ConditionCAPIManagement is set when the CAPI Cluster resource is being managed.
	ConditionCAPIManagement = "CAPIManagement"

	// ConditionForeignFinalizers is set while waiting for non-provider finalizers to clear.
	ConditionForeignFinalizers = "ForeignFinalizers"

	// ReasonWaitingForDeletion is used when deletion is in progress.
	ReasonWaitingForDeletion = "WaitingForDeletion"

	// ReasonCAPIInteractionProblem is used when interacting with CAPI objects fails.
	ReasonCAPIInteractionProblem = "CAPIInteractionProblem"

	// provisioningRequeueInterval is how often to recheck a provisioning cluster.
	provisioningRequeueInterval = 30 * time.Second
)
