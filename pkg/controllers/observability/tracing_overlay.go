package observability

import (
	"fmt"

	obsv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/observability/v1alpha1"
)

func addOtelCollector(overlay *Overlay, instance *obsv1alpha1.ObservabilityInstaller, cfg OperatorInstallConfig) error {
	overlay.AddComponent("../../components/collectors/tracing/operator")
	overlay.AddComponent("../../components/collectors/tracing/resources")
	addSubscriptionPatch(overlay, "opentelemetry-product", cfg)

	endpoint := fmt.Sprintf(
		"https://tempo-%s-gateway.%s.svc.cluster.local:8080/api/traces/v1/%s",
		tempoStackResourceName, instance.Namespace, tenantName,
	)
	patch := fmt.Sprintf(`apiVersion: opentelemetry.io/v1beta1
kind: OpenTelemetryCollector
metadata:
  name: cluster-collector
spec:
  config:
    exporters:
      otlphttp/tempo:
        endpoint: %s
`, endpoint)

	overlay.AddPatch("patches/opentelemetrycollector.yaml", []byte(patch))
	return nil
}

func addTempoStack(overlay *Overlay, instance *obsv1alpha1.ObservabilityInstaller, cfg OperatorInstallConfig) error {
	overlay.AddComponent("../../components/stores/tempostack/operator")
	overlay.AddComponent("../../components/stores/tempostack/resources")
	addSubscriptionPatch(overlay, "tempo-product", cfg)

	storage := instance.Spec.GetCapabilities().GetTracing().GetStorage()
	oss := storage.GetObjectStorageSpec()

	storageType := toTempoStorageType(oss)
	credentialMode := toTempoCredentialMode(oss)
	secretName := tempoSecretName(instance.Name)

	patch := fmt.Sprintf(`apiVersion: tempo.grafana.com/v1alpha1
kind: TempoStack
metadata:
  name: tempostack
spec:
  storage:
    secret:
      type: %s
      credentialMode: %s
      name: %s
`, storageType, credentialMode, secretName)

	if oss != nil {
		tls := oss.GetTLS()
		enableTLS := tls != nil || s3hasHTTPSEndpoint(*oss)
		if enableTLS {
			patch += "    tls:\n      enabled: true\n"
			if tls != nil {
				if tls.CAConfigMap != nil {
					patch += fmt.Sprintf("      ca: %s\n", tempoStorageCAConfigMapName(instance.Name))
				}
				if tls.CertSecret != nil {
					patch += fmt.Sprintf("      cert: %s\n", tempoStorageSecretName(instance.Name))
				}
				if tls.MinVersion != "" {
					patch += fmt.Sprintf("      minVersion: %s\n", tls.MinVersion)
				}
			}
		}
	}

	overlay.AddPatch("patches/tempostack.yaml", []byte(patch))
	return nil
}

func addTracingUIPlugin(overlay *Overlay) {
	overlay.AddComponent("../../components/console/tracing")
}

func addSubscriptionPatch(overlay *Overlay, subscriptionName string, cfg OperatorInstallConfig) {
	if cfg.StartingCSV == "" && cfg.Channel == "" {
		return
	}
	patch := fmt.Sprintf(`apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: %s
spec:
`, subscriptionName)

	if cfg.StartingCSV != "" {
		patch += fmt.Sprintf("  startingCSV: %s\n", cfg.StartingCSV)
	}
	if cfg.Channel != "" {
		patch += fmt.Sprintf("  channel: %s\n", cfg.Channel)
	}
	overlay.AddPatch(fmt.Sprintf("patches/subscription-%s.yaml", subscriptionName), []byte(patch))
}
