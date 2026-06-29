package uiplugin

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"

	uiv1alpha1 "github.com/rhobs/observability-operator/pkg/apis/uiplugin/v1alpha1"
	"github.com/rhobs/observability-operator/pkg/controllers/observability"
	"github.com/rhobs/observability-operator/pkg/reconciler"
)

type UIPluginBuildConfig struct {
	Images         map[string]string
	Namespace      string
	ClusterVersion string
	TLSCiphers     []string
	TLSMinVersion  string
	// Pre-resolved values for plugins that need cluster state.
	// Empty strings use defaults.
	LokiStackName      string
	LokiStackNamespace string
	LokiServiceNames   map[string]string
	TempoServiceNames  map[string]string
}

func BuildUIPluginOverlay(plugin *uiv1alpha1.UIPlugin, conf UIPluginBuildConfig, logger logr.Logger) (*observability.Overlay, *UIPluginInfo, error) {
	compatibilityInfo, err := lookupImageAndFeatures(plugin.Spec.Type, conf.ClusterVersion)
	if err != nil {
		return nil, nil, err
	}

	image := conf.Images[compatibilityInfo.ImageKey]
	if image == "" {
		return nil, nil, fmt.Errorf("no image provided for plugin type %s with key %s", plugin.Spec.Type, compatibilityInfo.ImageKey)
	}

	namespace := conf.Namespace
	features := slices.Clone(compatibilityInfo.Features)

	var pluginInfo *UIPluginInfo
	var pluginInfoErr error

	switch plugin.Spec.Type {
	case uiv1alpha1.TypeDashboards:
		pluginInfo, pluginInfoErr = createDashboardsPluginInfo(plugin, namespace, plugin.Name, image)

	case uiv1alpha1.TypeDistributedTracing:
		pluginInfo, pluginInfoErr = createDistributedTracingPluginInfo(plugin, namespace, plugin.Name, image, features)

	case uiv1alpha1.TypeLogging:
		lokiName := conf.LokiStackName
		lokiNs := conf.LokiStackNamespace
		if lokiName == "" {
			lokiName = resolveLokiStackNameFromCR(plugin)
		}
		if lokiNs == "" {
			lokiNs = OpenshiftLoggingNs
		}
		pluginInfo, pluginInfoErr = createLoggingPluginInfo(plugin, namespace, plugin.Name, image, features, lokiName, lokiNs, conf.Images["korrel8r"])

	case uiv1alpha1.TypeTroubleshootingPanel:
		pluginInfo, pluginInfoErr = createTroubleshootingPanelPluginInfo(plugin, namespace, plugin.Name, image, features, conf.ClusterVersion, logger)
		if pluginInfoErr == nil {
			pluginInfo.Korrel8rImage = conf.Images["korrel8r"]
			if conf.LokiServiceNames != nil {
				pluginInfo.LokiServiceNames = conf.LokiServiceNames
			}
			if conf.TempoServiceNames != nil {
				pluginInfo.TempoServiceNames = conf.TempoServiceNames
			}
		}

	case uiv1alpha1.TypeMonitoring:
		pluginInfo, pluginInfoErr = createMonitoringPluginInfo(plugin, namespace, plugin.Name, image, features, conf.ClusterVersion, conf.Images["health-analyzer"], conf.Images["perses"])

	default:
		return nil, nil, fmt.Errorf("plugin type not supported: %s", plugin.Spec.Type)
	}

	if pluginInfo == nil {
		if pluginInfoErr != nil {
			return nil, nil, pluginInfoErr
		}
		return nil, nil, fmt.Errorf("failed to build plugin info for %s", plugin.Spec.Type)
	}

	pluginInfo.TLSMinVersion = conf.TLSMinVersion
	pluginInfo.TLSCiphers = conf.TLSCiphers

	overlay := observability.NewOverlay(namespace)

	addResource := func(name string, obj client.Object) {
		data, err := kyaml.Marshal(obj)
		if err != nil {
			return
		}
		overlay.AddResource("resources/"+name, data)
	}

	addResource("serviceaccount.yaml", newServiceAccount(pluginInfo.Name, namespace))
	addResource("deployment.yaml", newDeployment(*pluginInfo, namespace, plugin.Spec.Deployment))
	addResource("service.yaml", newService(*pluginInfo, namespace))

	if IsVersionAheadOrEqual(conf.ClusterVersion, "v4.19") {
		addResource("consoleplugin.yaml", newConsolePlugin(*pluginInfo, namespace))
	} else if IsVersionAheadOrEqual(conf.ClusterVersion, "v4.17") {
		addResource("consoleplugin.yaml", newRhobsConsolePlugin(*pluginInfo, namespace))
	} else {
		addResource("consoleplugin.yaml", newLegacyConsolePlugin(*pluginInfo, namespace))
	}

	if pluginInfo.Role != nil {
		addResource("role.yaml", newRole(*pluginInfo))
	}
	if pluginInfo.RoleBinding != nil {
		addResource("rolebinding.yaml", newRoleBinding(*pluginInfo))
	}
	if pluginInfo.ConfigMap != nil {
		addResource("configmap.yaml", pluginInfo.ConfigMap)
	}

	for i, role := range pluginInfo.ClusterRoles {
		if role != nil {
			addResource(fmt.Sprintf("clusterrole-%d.yaml", i), role)
		}
	}
	for i, binding := range pluginInfo.ClusterRoleBindings {
		if binding != nil {
			addResource(fmt.Sprintf("clusterrolebinding-%d.yaml", i), binding)
		}
	}

	if (plugin.Spec.Type == uiv1alpha1.TypeTroubleshootingPanel || plugin.Spec.Type == uiv1alpha1.TypeLogging) && pluginInfo.Korrel8rImage != "" {
		if err := addKorrel8rComponent(overlay, *pluginInfo); err != nil {
			return nil, nil, err
		}
	}

	if plugin.Spec.Type == uiv1alpha1.TypeMonitoring {
		monitoringConfig := plugin.Spec.Monitoring
		serviceAccountName := plugin.Name + serviceAccountSuffix
		incidentsEnabled := monitoringConfig != nil &&
			monitoringConfig.Incidents != nil &&
			monitoringConfig.Incidents.Enabled &&
			pluginInfo.HealthAnalyzerImage != ""

		healthAnalyzerEnabled := monitoringConfig != nil &&
			monitoringConfig.ClusterHealthAnalyzer != nil &&
			monitoringConfig.ClusterHealthAnalyzer.Enabled &&
			pluginInfo.HealthAnalyzerImage != ""

		deployHealthAnalyzer := incidentsEnabled || healthAnalyzerEnabled
		if deployHealthAnalyzer {
			addResource("ha-clusterrole.yaml", componentsHealthClusterRole("components-health-view"))
			addResource("ha-clusterrolebinding-components.yaml", newClusterRoleBinding(namespace, serviceAccountName, "components-health-view", plugin.Name+"-components-health-view"))
			addResource("ha-configmap.yaml", newComponentHealthConfig(namespace))
			addResource("ha-clusterrolebinding-monitoring.yaml", newClusterRoleBinding(namespace, serviceAccountName, "cluster-monitoring-view", plugin.Name+"cluster-monitoring-view"))
			addResource("ha-clusterrolebinding-auth.yaml", newClusterRoleBinding(namespace, serviceAccountName, "system:auth-delegator", serviceAccountName+"-system-auth-delegator"))
			addResource("ha-alertmanager-rolebinding.yaml", newAlertManagerViewRoleBinding(serviceAccountName, namespace))
			addResource("ha-prometheus-role.yaml", newHealthAnalyzerPrometheusRole(namespace))
			addResource("ha-prometheus-rolebinding.yaml", newHealthAnalyzerPrometheusRoleBinding(namespace))
			addResource("ha-service.yaml", newHealthAnalyzerService(namespace))
			addResource("ha-deployment.yaml", newHealthAnalyzerDeployment(namespace, serviceAccountName, *pluginInfo))
			addResource("ha-servicemonitor.yaml", newHealthAnalyzerServiceMonitor(namespace))
		}

		persesEnabled := monitoringConfig != nil && monitoringConfig.Perses != nil && monitoringConfig.Perses.Enabled
		if persesEnabled {
			persesServiceAccountName := "perses" + serviceAccountSuffix
			addResource("perses-serviceaccount.yaml", newServiceAccount("perses", namespace))
			addResource("perses-clusterrolebinding-auth.yaml", newClusterRoleBinding(namespace, persesServiceAccountName, "system:auth-delegator", persesServiceAccountName+"-system-auth-delegator"))
			addResource("perses-clusterrole.yaml", newPersesClusterRole())
			addResource("perses-clusterrolebinding.yaml", newClusterRoleBinding(namespace, persesServiceAccountName, "perses-cr", persesServiceAccountName+"-perses-cr"))
			addResource("perses.yaml", newPerses(namespace, pluginInfo.PersesImage))
			addResource("perses-datasource.yaml", newAcceleratorsDatasource(namespace))

			acceleratorsDashboard, err := newAcceleratorsDashboard(namespace)
			if err == nil {
				addResource("perses-accelerators-dashboard.yaml", acceleratorsDashboard)
			}
			apmDashboard, err := newAPMDashboard(namespace)
			if err == nil {
				addResource("perses-apm-dashboard.yaml", apmDashboard)
			}
		}
	}

	return overlay, pluginInfo, pluginInfoErr
}

func resolveLokiStackNameFromCR(plugin *uiv1alpha1.UIPlugin) string {
	if plugin.Spec.Logging != nil && plugin.Spec.Logging.LokiStack != nil && plugin.Spec.Logging.LokiStack.Name != "" {
		return plugin.Spec.Logging.LokiStack.Name
	}
	return DefaultLokiStackName
}

func addKorrel8rComponent(overlay *observability.Overlay, info UIPluginInfo) error {
	overlay.AddComponent("../../components/korrel8r/resources")

	imagePatch := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: korrel8r
spec:
  template:
    spec:
      containers:
        - name: korrel8r
          image: %s
`, info.Korrel8rImage)
	overlay.AddPatch("patches/korrel8r-deployment.yaml", []byte(imagePatch))

	configYAML, err := generateKorrel8rConfig(info)
	if err != nil {
		return err
	}
	var indented strings.Builder
	for _, line := range strings.Split(configYAML, "\n") {
		if line != "" {
			indented.WriteString("    ")
			indented.WriteString(line)
		}
		indented.WriteString("\n")
	}
	cmPatch := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: korrel8r
data:
  korrel8r.yaml: |
%s`, indented.String())
	overlay.AddPatch("patches/korrel8r-configmap.yaml", []byte(cmPatch))

	return nil
}

func generateKorrel8rConfig(info UIPluginInfo) (string, error) {
	korrel8rData := map[string]string{
		"Metric":      "thanos-querier",
		"MetricAlert": "alertmanager-main",
		"Log":         "logging-loki-gateway-http",
		"Netflow":     "loki-gateway-http",
		"Trace":       "tempo-platform-gateway",
		"MonitoringNs": reconciler.OpenshiftMonitoringNamespace,
		"LoggingNs":    OpenshiftLoggingNs,
		"NetobservNs":  OpenshiftNetobservNs,
		"TracingNs":    OpenshiftTracingNs,
	}

	if info.LokiServiceNames[OpenshiftLoggingNs] != "" {
		korrel8rData["Log"] = info.LokiServiceNames[OpenshiftLoggingNs]
	}
	if info.LokiServiceNames[OpenshiftNetobservNs] != "" {
		korrel8rData["Netflow"] = info.LokiServiceNames[OpenshiftNetobservNs]
	}
	if info.TempoServiceNames[OpenshiftTracingNs] != "" {
		korrel8rData["Trace"] = info.TempoServiceNames[OpenshiftTracingNs]
	}

	tmpl := template.Must(template.ParseFS(korrel8rConfigYAMLTmplFile, "config/korrel8r.yaml"))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, korrel8rData); err != nil {
		return "", err
	}
	return buf.String(), nil
}
