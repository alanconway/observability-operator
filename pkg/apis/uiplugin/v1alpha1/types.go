// +groupName=observability.openshift.io
// +kubebuilder:rbac:groups=observability.openshift.io,resources=uiplugins,verbs=list;get;watch
// +kubebuilder:rbac:groups=observability.openshift.io,resources=uiplugins/status;uiplugins/finalizers,verbs=get;update

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// UIPlugin defines an observability console plugin.
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
type UIPlugin struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UIPluginSpec   `json:"spec,omitempty"`
	Status UIPluginStatus `json:"status,omitempty"`
}

// UIPluginList contains a list of UIPlugin
// +kubebuilder:resource
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type UIPluginList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []UIPlugin `json:"items"`
}

// +kubebuilder:validation:Enum=Dashboards;TroubleshootingPanel;DistributedTracing
type UIPluginType string

const (
	// TypeDashboards deploys the Dashboards Dynamic Plugin for OpenShift Console.
	TypeDashboards UIPluginType = "Dashboards"
	// DistributedTracing deploys the Distributed Tracing Dynamic Plugin for the OpenShift Console
	TypeDistributedTracing UIPluginType = "DistributedTracing"
	// TroubleshootingPanel deploys the Troubleshooting Panel Dynamic Plugin for the OpenShift Console
	TypeTroubleshootingPanel UIPluginType = "TroubleshootingPanel"
)

// TroubleshootingPanelConfig contains options for configuring the Troubleshooting Panel  plugin
type TroubleshootingPanelConfig struct {
	// Timeout is the maximum duration before a query timeout.
	//
	// The value is expected to be a sequence of digits followed by a unit suffix, which can be 's' (seconds)
	// or 'm' (minutes).
	//
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="OCP Console Query Timeout",xDescriptors={"urn:alm:descriptor:com.tectonic.ui:ocpConsoleTimeout"}
	// +kubebuilder:validation:Pattern:="^([0-9]+)([sm]{1})$"
	Timeout string `json:"timeout,omitempty"`
	// korrel8r defines the Korrel8r instance that the troubleshooting panel plugin will connect to
	//
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Korrel8r Instance"
	Korrel8r TroubleshootingPanelKorrel8rConfig `json:"korrel8r,omitempty"`
}

type TroubleshootingPanelKorrel8rConfig struct {
	// Name of the korrel8r instance
	//
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Korrel8r Instance Name"
	Name string `json:"name,omitempty"`

	// Namespace of the korrel8r instance
	//
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Korrel8r Instance Namespace"
	Namespace string `json:"namespace,omitempty"`
}

// DistributedTracingConfig contains options for configuring the Distributed Tracing plugin
type DistributedTracingConfig struct {
	// Timeout is the maximum duration before a query timeout.
	//
	// The value is expected to be a sequence of digits followed by a unit suffix, which can be 's' (seconds)
	// or 'm' (minutes).
	//
	// +optional
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="OCP Console Query Timeout",xDescriptors={"urn:alm:descriptor:com.tectonic.ui:ocpConsoleTimeout"}
	// +kubebuilder:validation:Pattern:="^([0-9]+)([sm]{1})$"
	Timeout string `json:"timeout,omitempty"`
}

// UIPluginSpec is the specification for desired state of UIPlugin.
type UIPluginSpec struct {
	// Type defines the UI plugin.
	// +required
	// +kubebuilder:validation:Required
	Type UIPluginType `json:"type"`

	// TroubleshootingPanel contains configuration for the troubleshooting console plugin.
	//
	// +kubebuilder:validation:Optional
	TroubleshootingPanel *TroubleshootingPanelConfig `json:"troubleshootingPanel,omitempty"`

	// DistributedTracing contains configuration for the distributed tracing console plugin.
	//
	// +kubebuilder:validation:Optional
	DistributedTracing *DistributedTracingConfig `json:"distributedTracing,omitempty"`
}

// UIPluginStatus defines the observed state of UIPlugin.
// It should always be reconstructable from the state of the cluster and/or outside world.
type UIPluginStatus struct {
	// Conditions provide status information about the plugin.
	// +listType=atomic
	Conditions []Condition `json:"conditions"`
}

type ConditionStatus string

// +required
// +kubebuilder:validation:Required
// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$`
// +kubebuilder:validation:MaxLength=316
type ConditionType string

const (
	ConditionTrue    ConditionStatus = "True"
	ConditionFalse   ConditionStatus = "False"
	ConditionUnknown ConditionStatus = "Unknown"

	ReconciledCondition        ConditionType = "Reconciled"
	AvailableCondition         ConditionType = "Available"
	ResourceDiscoveryCondition ConditionType = "ResourceDiscovery"
)

type Condition struct {
	// type of condition in CamelCase or in foo.example.com/CamelCase.
	// The regex it matches is (dns1123SubdomainFmt/)?(qualifiedNameFmt)
	Type ConditionType `json:"type"`
	// observedGeneration represents the .metadata.generation that the condition was set based upon.
	// For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
	// with respect to the current state of the instance.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// lastTransitionTime is the last time the condition transitioned from one status to another.
	// This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
	// reason contains a programmatic identifier indicating the reason for the condition's last transition.
	// Producers of specific condition types may define expected values and meanings for this field,
	// and whether the values are considered a guaranteed API.
	// The value should be a CamelCase string.
	// This field may not be empty.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`
	Reason string `json:"reason"`
	// message is a human readable message indicating details about the transition.
	// This may be an empty string.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message"`
	// status of the condition
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=True;False;Unknown;Degraded
	Status ConditionStatus `json:"status"`
}

func (c Condition) Equal(n Condition) bool {
	if c.Reason == n.Reason && c.Status == n.Status && c.Message == n.Message && c.ObservedGeneration == n.ObservedGeneration {
		return true
	}
	return false
}
