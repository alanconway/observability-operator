package observability

import (
	"fmt"

	obsv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/observability/v1alpha1"
)

type OverlayConfig struct {
	OpenTelemetryOperator OperatorInstallConfig
	TempoOperator         OperatorInstallConfig
}

func BuildOverlay(instance *obsv1alpha1.ObservabilityInstaller, cfg OverlayConfig) (*Overlay, error) {
	if instance.Namespace == "" {
		instance.Namespace = "default"
	}

	overlay := NewOverlay(instance.Namespace)

	tracing := instance.Spec.GetCapabilities().GetTracing()
	if tracing != nil && tracing.Enabled {
		if err := addOtelCollector(overlay, instance, cfg.OpenTelemetryOperator); err != nil {
			return nil, fmt.Errorf("building OTel collector overlay: %w", err)
		}
		if err := addTempoStack(overlay, instance, cfg.TempoOperator); err != nil {
			return nil, fmt.Errorf("building TempoStack overlay: %w", err)
		}
		addTracingUIPlugin(overlay)
	} else if tracing != nil && tracing.GetOperators() != nil &&
		tracing.GetOperators().Install != nil && *tracing.GetOperators().Install {
		// Install operators only (subscriptions + namespaces + operatorgroups), without operand instances.
		overlay.AddComponent("../../components/collectors/tracing/operator")
		addSubscriptionPatch(overlay, "opentelemetry-product", cfg.OpenTelemetryOperator)
		overlay.AddComponent("../../components/stores/tempostack/operator")
		addSubscriptionPatch(overlay, "tempo-product", cfg.TempoOperator)
	}

	return overlay, nil
}
