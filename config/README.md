# Kustomization for operator and generator

This project provides a traditional `operator` and a command line `generator`.
The generator takes the operator's CRs as input, and produces the same resources as the operator would as YAML files.
The generator output can be applied applied directly by `kubectl` without using the operator.

- You can deploy the generated YAML directly in situations where you don't want the operator.
  - Generate once, apply many times in a multi-cluster.
  - Apply to constrained edge nodes that don't run the operator.
  - Build one-time install wizards, assisted installers that don't use an operator.
- Domain experts can contribute to the resources without understanding the controller code.
- Users can create their own modified versions of the stack and deploy with or without the operator.

The same code is used by the operator's controller and the generator to convert COO custom resources into
kustomize overlays that edit base manifests to build the final resources.
See [Generated overlay](#generated-overlay).

## Directory layout

- `stack/`: Defines the observability stack, the output of the operator/generator.
  - `components/`: Optional components in the observability stack
    - `collectors/`: Resources that collect observability data, and their operators
      - `tracing/`: OpenTelemetryCollector ...
    - `stores/`: Resources that store observability data, and their operators
      - `tempostack`
- `samples/`: Production-ready samples of COO resources for specific scenarios. \
  These resources can be deployed for the operator or used as input to the generator.
  - `single/`: Typical single-cluster full observability stack

**NOTE**: Each component has two sub-directories:

- `operator/` (optional) — Namespace, OperatorGroup, and Subscription to install the operator via OLM.
- `resources/` — Operand CRs and supporting resources (RBAC, secrets, configmaps).

## Generated overlay

The Go controller and generator build an in-memory kustomize overlay using `sigs.k8s.io/kustomize/api/krusty`.
The Go controller applies the overlay to reconcile live resources, the generator writes the resources to stdout.

The key files:

- `stack/fs.go` — embeds `base/` and `components/` into the binary via `//go:embed`.
- `pkg/controllers/observability/overlay.go` — `Overlay` type that populates an
  in-memory `filesys.FileSystem` with the embedded manifests plus generated patches
  and resources, then runs `krusty.MakeKustomizer().Run()` to produce `[]client.Object`.
- `pkg/controllers/observability/bundle.go` — `BuildOverlay()` entry point that
  selectively adds components based on the `ObservabilityInstaller` spec.
- `pkg/controllers/observability/tracing_overlay.go` — adds tracing components
  (OTEL collector, TempoStack, Subscriptions, UIPlugin) with dynamic patches.

The generated overlay is equivalent to:

```
generated/
├── kustomization.yaml               # components + patches + resources
├── namespace-transformer.yaml       # sets namespace from instance
├── patches/
│   ├── tempostack.yaml              # dynamic storage config (type, credentials, TLS)
│   ├── opentelemetrycollector.yaml   # dynamic tempo endpoint
│   ├── subscription-*.yaml          # startingCSV/channel patches (conditional)
└── resources/
    └── uiplugin-*.yaml              # UIPlugin resources
```

Hard-coded values live in the base component manifests.
The overlay patches dynamic fields derived from the `ObservabilityInstaller` spec.
Secrets are the exception — they require cluster reads and are assembled in Go code
(`tempoStackSecrets()` in `tempo_components.go`). The generator emits placeholder
secrets with stderr warnings.

### Status

Tracing resources are built via kustomize overlay:
- TempoStack, OpenTelemetryCollector, RBAC, UIPlugin, Subscriptions
- Operator subscriptions are patched for `startingCSV` and `channel` via overlay
- Secrets remain as Go code (need cluster reads for credential assembly)

The generator (`cmd/generator`) supports the same capabilities as the controller,
including `--opentelemetry-csv` and `--tempo-csv` flags for subscription versioning.

## To be done

- For standalone (non-controller) use, add example storage secrets or a
  documented manual step for providing S3/Azure/GCS credentials.
- Scripts to apply with kubectl in 2 phases, operators first, wait for CSVs, then operands.


