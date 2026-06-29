package observability

import (
	"context"
	"fmt"
	"strings"

	olmv1alpha1 "github.com/operator-framework/api/pkg/operators/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	obsv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/observability/v1alpha1"
	"github.com/rhobs/observability-operator/pkg/controllers/util"
	"github.com/rhobs/observability-operator/pkg/reconciler"
)

type operatorsStatus struct {
	cooNamespace string
	subs         []olmv1alpha1.Subscription
}

func (s *operatorsStatus) ShouldInstall(operatorName string) bool {
	for _, sub := range s.subs {
		if strings.HasPrefix(sub.Name, operatorName) && sub.Labels[util.ResourceLabel] != util.OpName {
			return false
		}
	}
	return true
}

func (s *operatorsStatus) cooManages(operatorName string) *olmv1alpha1.Subscription {
	for _, sub := range s.subs {
		if strings.HasPrefix(sub.Name, operatorName) && sub.Labels[util.ResourceLabel] == util.OpName {
			return &sub
		}
	}
	return nil
}

func overlayConfig(opts Options) OverlayConfig {
	return OverlayConfig{
		OpenTelemetryOperator: opts.OpenTelemetryOperator,
		TempoOperator:         opts.TempoOperator,
	}
}

func isSubscription(obj client.Object) bool {
	gvk := obj.GetObjectKind().GroupVersionKind()
	return gvk.Group == "operators.coreos.com" && gvk.Kind == "Subscription"
}

func getReconcilers(ctx context.Context, k8sClient client.Client, k8sReader client.Reader, instance *obsv1alpha1.ObservabilityInstaller, opts Options, operatorsStatus operatorsStatus) ([]reconciler.Reconciler, error) {
	var reconcilers []reconciler.Reconciler
	cfg := overlayConfig(opts)

	// Build overlay for the universe of all possible objects (for cleanup).
	allInstance := instance.DeepCopy()
	if allInstance.Spec.Capabilities == nil {
		allInstance.Spec.Capabilities = &obsv1alpha1.CapabilitiesSpec{}
	}
	if allInstance.Spec.Capabilities.Tracing == nil {
		allInstance.Spec.Capabilities.Tracing = &obsv1alpha1.TracingSpec{}
	}
	allInstance.Spec.Capabilities.Tracing.Enabled = true
	allOverlay, err := BuildOverlay(allInstance, cfg)
	if err != nil {
		return nil, fmt.Errorf("building full overlay: %w", err)
	}
	allObjects, err := allOverlay.Build()
	if err != nil {
		return nil, fmt.Errorf("building full overlay objects: %w", err)
	}

	// Build overlay for the currently desired state.
	currentOverlay, err := BuildOverlay(instance, cfg)
	if err != nil {
		return nil, fmt.Errorf("building overlay: %w", err)
	}
	currentObjects, err := currentOverlay.Build()
	if err != nil {
		return nil, fmt.Errorf("building overlay objects: %w", err)
	}

	// Build secrets (need cluster reads, not in overlay).
	var secretObjects []client.Object
	if tracing := instance.Spec.GetCapabilities().GetTracing(); tracing != nil && tracing.Enabled {
		secrets, err := tempoStackSecrets(ctx, k8sClient, k8sReader, *instance)
		if err != nil {
			return nil, fmt.Errorf("failed to create TempoStack secret: %w", err)
		}
		if secrets.objectStorage != nil {
			secretObjects = append(secretObjects, secrets.objectStorage)
		}
		if secrets.objectStorageTLSSecret != nil {
			secretObjects = append(secretObjects, secrets.objectStorageTLSSecret)
		}
		if secrets.objectStorageCAConfigMap != nil {
			secretObjects = append(secretObjects, secrets.objectStorageCAConfigMap)
		}
	}

	// Handle deletion of the entire instance.
	if instance.ObjectMeta.DeletionTimestamp != nil {
		for _, obj := range allObjects {
			reconcilers = append(reconcilers, reconciler.NewDeleter(obj))
		}
		for _, obj := range secretObjects {
			reconcilers = append(reconcilers, reconciler.NewDeleter(obj))
		}
		if otelSub := operatorsStatus.cooManages("opentelemetry"); otelSub != nil {
			reconcilers = append(reconcilers, reconciler.NewDeleter(otelSub))
			reconcilers = append(reconcilers, reconciler.NewDeleter(
				&olmv1alpha1.ClusterServiceVersion{
					ObjectMeta: metav1.ObjectMeta{
						Name:      otelSub.Status.CurrentCSV,
						Namespace: otelSub.Namespace,
					},
				}))
		}
		if tempoSub := operatorsStatus.cooManages("tempo"); tempoSub != nil {
			reconcilers = append(reconcilers, reconciler.NewDeleter(tempoSub))
			reconcilers = append(reconcilers, reconciler.NewDeleter(
				&olmv1alpha1.ClusterServiceVersion{
					ObjectMeta: metav1.ObjectMeta{
						Name:      tempoSub.Status.CurrentCSV,
						Namespace: tempoSub.Namespace,
					},
				}))
		}
		return reconcilers, nil
	}

	// Track what we install, so we can delete the rest.
	installedObjects := map[string]client.Object{}

	// Install/update enabled objects.
	tracing := instance.Spec.GetCapabilities().GetTracing()
	if tracing != nil && tracing.Enabled {
		for _, obj := range currentObjects {
			if isSubscription(obj) {
				if operatorsStatus.ShouldInstall(obj.GetName()) {
					reconcilers = append(reconcilers, reconciler.NewCreateUpdateReconciler(obj, instance))
					installedObjects[gvkNameIdentifier(obj)] = obj
				}
			} else {
				reconcilers = append(reconcilers, reconciler.NewUpdater(obj, instance))
				installedObjects[gvkNameIdentifier(obj)] = obj
			}
		}
		for _, obj := range secretObjects {
			reconcilers = append(reconcilers, reconciler.NewUpdater(obj, instance))
			installedObjects[gvkNameIdentifier(obj)] = obj
		}
	}

	// Install operators only (without operand instances).
	if tracing != nil &&
		tracing.GetOperators() != nil &&
		tracing.GetOperators().Install != nil && *tracing.GetOperators().Install {
		for _, obj := range currentObjects {
			if isSubscription(obj) {
				if operatorsStatus.ShouldInstall(obj.GetName()) {
					reconcilers = append(reconcilers, reconciler.NewCreateUpdateReconciler(obj, instance))
					installedObjects[gvkNameIdentifier(obj)] = obj
				}
			}
		}
	}

	// Delete objects that exist in the full set but are not currently installed.
	for _, obj := range allObjects {
		if installedObjects[gvkNameIdentifier(obj)] == nil {
			reconcilers = append(reconcilers, reconciler.NewDeleter(obj))
		}
	}

	// Delete CSV explicitly when subscriptions are removed.
	otelSubs := findByName(allObjects, "opentelemetry-product")
	if otelSub := operatorsStatus.cooManages("opentelemetry"); otelSub != nil && (otelSubs == nil || installedObjects[gvkNameIdentifier(otelSubs)] == nil) {
		reconcilers = append(reconcilers, reconciler.NewDeleter(otelSub))
		reconcilers = append(reconcilers, reconciler.NewDeleter(
			&olmv1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      otelSub.Status.CurrentCSV,
					Namespace: otelSub.Namespace,
				},
			}))
	}
	tempoSub := findByName(allObjects, "tempo-product")
	if managedTempoSub := operatorsStatus.cooManages("tempo"); managedTempoSub != nil && (tempoSub == nil || installedObjects[gvkNameIdentifier(tempoSub)] == nil) {
		reconcilers = append(reconcilers, reconciler.NewDeleter(managedTempoSub))
		reconcilers = append(reconcilers, reconciler.NewDeleter(
			&olmv1alpha1.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{
					Name:      managedTempoSub.Status.CurrentCSV,
					Namespace: managedTempoSub.Namespace,
				},
			}))
	}

	return reconcilers, nil
}

func findByName(objects []client.Object, name string) client.Object {
	for _, obj := range objects {
		if obj.GetName() == name {
			return obj
		}
	}
	return nil
}

func gvkNameIdentifier(obj client.Object) string {
	return fmt.Sprintf("%s/%s", obj.GetObjectKind().GroupVersionKind().String(), obj.GetName())
}
