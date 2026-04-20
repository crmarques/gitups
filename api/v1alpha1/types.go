// Package v1alpha1 defines the on-disk schemas for gitups: Provision (lean
// user intent), FullProvision (resolved source of truth), and
// PackageDefinition (package.yaml).
package v1alpha1

const (
	APIVersion = "gitups/v1alpha1"

	KindProvision         = "Provision"
	KindFullProvision     = "FullProvision"
	KindPackageDefinition = "PackageDefinition"
	KindPackageDescriptor = "PackageDescriptor"

	// PlaceholderSentinel is the literal string written into resolvedValues for
	// any field the user must supply. Scanners walk resolved values looking for
	// this sentinel to build the placeholders list.
	PlaceholderSentinel = "__GITUPS_PLACEHOLDER__"

	UnitTypeInstall  = "install"
	UnitTypeResource = "resource"
)

// Role identifies a package's position in the controller topology. Every
// package declares exactly one. The string values are the long kebab form
// so stand-alone contexts (role values, directory names, JSON kind) are
// self-describing; the Go constants stay short for contributor ergonomics.
type Role string

const (
	// RoleKRC: Kubernetes Resource Controller (e.g. ArgoCD, Flux).
	// Installs packages by syncing a git source of truth into cluster
	// manifests. A KRC package carries a "kubernetes-resource-controller/"
	// domain directory whose children implement KRC intents (managed-repo,
	// …).
	RoleKRC Role = "kubernetes-resource-controller"
	// RoleSRC: Services Resource Controller (e.g. Declarest, Crossplane).
	// Configures services and bindings by syncing a git source of truth
	// into managed-service state. An SRC package carries a
	// "service-resource-controller/" domain directory whose children
	// implement SRC intents (managed-resource, managed-script, …).
	RoleSRC Role = "service-resource-controller"
	// RoleWorkload: every other package — applications, infrastructure
	// building blocks, capability providers and consumers.
	RoleWorkload Role = "workload"
)

// Domain identifies a top-level directory gitups understands inside a
// package. Presence of the directory is the opt-in; the value at that key
// is the set of child units keyed by sub-name (renderer / template /
// intent). DomainServiceConfig is not a package-level directory — it is
// the virtual domain carried on a ResolvedPackage routed to a
// service-resources repo.
const (
	DomainInstall       = "install"
	DomainResources     = "resources"
	DomainKRC           = "kubernetes-resource-controller"
	DomainSRC           = "service-resource-controller"
	DomainServiceConfig = "service-config"
)

// Repository type values accepted by Provision.spec.repositories[].type.
// Generic vs env is inferred from repoRef presence: env repos carry
// repoRef, generic repos do not.
const (
	// RepoTypeKubernetesResources: any repo whose contents are K8s
	// manifests. The only supported repo type.
	RepoTypeKubernetesResources = "kubernetes-resources"
)

// Renderer identifiers. Every install method and resource descriptor must pick
// one.
const (
	RendererOLM       = "olm"
	RendererKustomize = "kustomize"
	RendererHelm      = "helm"
	RendererRaw       = "raw"
)

type ObjectMeta struct {
	Name string `json:"name"`
}

// Provision is the lean, user-authored intent file. It carries sources and a
// repository-centric declaration of package installs and resources.
type Provision struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   ObjectMeta    `json:"metadata"`
	Spec       ProvisionSpec `json:"spec"`
}

type ProvisionSpec struct {
	// EnvKey is the value substituted for {{.Env}} in repository names. When
	// empty, {{.Env}} falls back to metadata.name.
	EnvKey      string          `json:"envKey,omitempty"`
	Extends     *Extends        `json:"extends,omitempty"`
	Controllers *Controllers    `json:"controllers,omitempty"`
	Sources     []PackageSource `json:"sources"`
	// Repositories enumerates output repos. Generic repos select package
	// installs; env repos select or derive package resources.
	Repositories []RepositoryDecl `json:"repositories,omitempty"`
}

// Controllers assigns the KRC and SRC roles to already-selected package
// instances elsewhere in the same Provision. Keys use camelCase because
// the surrounding "controllers:" object makes the concept clear.
type Controllers struct {
	KubernetesResources *ControllerAssignment `json:"kubernetesResources,omitempty"`
	ServiceResources    *ControllerAssignment `json:"serviceResources,omitempty"`
}

// ControllerAssignment points at a package selected by a repository
// elsewhere in the same Provision. Shape matches BindingRef.Provider so
// there is one reference spelling for "a package selected in this
// Provision."
type ControllerAssignment struct {
	// Repo names a repository declared in spec.repositories. {{.Env}}
	// substitution applies before matching.
	Repo string `json:"repo"`
	// Instance matches a packages[].instance (defaults to the template
	// basename) inside the referenced repository.
	Instance string `json:"instance"`
}

// Extends points at a generic Provision whose sources and repositories are
// merged under the env Provision's own entries. Single-level only; a base
// Provision must not itself carry spec.extends.
type Extends struct {
	// Source is the base Provision location. Supported forms:
	//   - filesystem path relative to the env provision.yaml
	//     (e.g. "../basic-infra/provision.yaml")
	//   - git+<url>#<path> with spec.extends.ref pinning a tag/commit/branch
	//     (not yet implemented; reserved)
	Source string `json:"source"`
	// Ref is an optional pin for git sources (tag, commit, or branch).
	// Ignored for filesystem sources today but persisted for traceability.
	Ref string `json:"ref,omitempty"`
}

type PackageSource struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
}

// PackageRef is a package entry under Provision.spec.repositories[*].packages.
type PackageRef struct {
	Template      string         `json:"template"`
	Instance      string         `json:"instance,omitempty"`
	Role          Role           `json:"role,omitempty"`
	InstallMethod string         `json:"installMethod,omitempty"`
	Values        map[string]any `json:"values,omitempty"`
	Resources     []ResourceRef  `json:"resources,omitempty"`
	// Bindings wires this consumer package to capability providers. Each
	// entry materializes a consumer-side resource in the repository this
	// ref belongs to and a provider-side resource in each env repo that
	// references the provider's generic repo.
	Bindings []BindingRef `json:"bindings,omitempty"`
}

// BindingRef instantiates a provider/consumer capability wiring under the
// consumer package entry in Provision.
type BindingRef struct {
	Name       string         `json:"name"`
	Capability string         `json:"capability"`
	Provider   ProviderRef    `json:"provider"`
	Values     map[string]any `json:"values,omitempty"`
}

// ProviderRef fully qualifies the provider package instance for a binding.
type ProviderRef struct {
	Repo     string `json:"repo"`
	Instance string `json:"instance"`
}

// ResourceRef selects one resource template instance for an env repository.
type ResourceRef struct {
	Template string         `json:"template"`
	Name     string         `json:"name"`
	Values   map[string]any `json:"values,omitempty"`
}

// RepositoryDecl names an output repo and lists which package installs or
// resources land in it. {{.Env}} is substituted in name.
type RepositoryDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Type        string         `json:"type"`
	RepoRef     *RepositoryRef `json:"repoRef,omitempty"`
	Packages    []PackageRef   `json:"packages,omitempty"`
}

type RepositoryRef struct {
	Name   string `json:"name"`
	Commit string `json:"commit,omitempty"`
}

// FullProvision is the resolved, reviewable source of truth. It is generated
// by `gitups expand` and edited by the user to fill placeholders before being
// consumed by `gitups render`.
type FullProvision struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Spec       FullProvisionSpec `json:"spec"`
}

type FullProvisionSpec struct {
	SourceProvisionRef ObjectMeta `json:"sourceProvisionRef"`
	// ExtendedFrom records the generic Provision this FullProvision was
	// merged against, if any. Advisory: render reads only resolved packages.
	ExtendedFrom *ExtendedFrom        `json:"extendedFrom,omitempty"`
	Sources      []PackageSource      `json:"sources"`
	Repository   RepositoryBlock      `json:"repository"`
	Repositories []ResolvedRepository `json:"repositories,omitempty"`
	Packages     []ResolvedPackage    `json:"packages"`
	Placeholders []Placeholder        `json:"placeholders"`
}

// ExtendedFrom is the advisory trace of a merge performed at load time.
type ExtendedFrom struct {
	Source string `json:"source"`
	Ref    string `json:"ref,omitempty"`
}

type RepositoryBlock struct {
	Layout     string `json:"layout"`
	OutputPath string `json:"outputPath"`
}

// ResolvedRepository is the expanded form of a RepositoryDecl with {{.Env}}
// substituted. Kept in FullProvision so render can honor user-authored
// descriptions and preserve repo ordering.
type ResolvedRepository struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Type        string         `json:"type,omitempty"`
	RepoRef     *RepositoryRef `json:"repoRef,omitempty"`
}

type ResolvedPackage struct {
	Template string `json:"template"`
	// PackageInstance is the service/package instance root used in rendered
	// paths. Instance remains the unique resolved unit name.
	PackageInstance string `json:"packageInstance,omitempty"`
	UnitType        string `json:"unitType"`
	// Domain names the package directory this unit was loaded from. One of
	// DomainInstall / DomainResources / DomainKRC / DomainSRC. Controls
	// the rendered output sub-path and which map in the catalog entry the
	// renderer looks the descriptor up in.
	Domain           string         `json:"domain"`
	InstallMethod    string         `json:"installMethod,omitempty"`
	ResourceTemplate string         `json:"resourceTemplate,omitempty"`
	ResourceName     string         `json:"resourceName,omitempty"`
	Repository       string         `json:"repository"`
	Instance         string         `json:"instance"`
	Role             Role           `json:"role"`
	Renderer         string         `json:"renderer"`
	DependsOn        []string       `json:"dependsOn,omitempty"`
	ResolvedValues   map[string]any `json:"resolvedValues"`
	RenderedPaths    RenderedPaths  `json:"renderedPaths"`
	// ApplyWave is the controller-agnostic apply order, computed from the
	// dependsOn DAG (roots = 0, otherwise max(deps)+1). Emitted as
	// `gitups.io/apply-wave` commonAnnotation on the rendered package.
	ApplyWave int `json:"applyWave"`
	// Binding, when set, marks this unit as synthesized by a capability
	// binding rather than an explicit Provision entry.
	Binding *BindingOrigin `json:"binding,omitempty"`
	// Controller, when set, points at the KRC or SRC package instance
	// that owns this unit at apply time. Populated for units routed
	// through a controller (installs behind a KRC, resources rewired
	// through an SRC). Nil for bootstrap-direct units.
	Controller *ControllerBinding `json:"controller,omitempty"`
}

// ControllerBinding records which KRC or SRC owns a resolved unit and the
// intent the controller implements to handle it.
type ControllerBinding struct {
	// Kind is the controller role: RoleKRC or RoleSRC.
	Kind Role `json:"kind"`
	// Instance is the controller package instance (the package selected
	// by spec.repositories[].packages[].instance).
	Instance string `json:"instance"`
	// Template is the controller package's qualified catalog key
	// (e.g. "local/declarest"). Carried so the renderer can look up
	// the controller's domain unit without reopening the Provision.
	Template string `json:"template,omitempty"`
	// Intent is the standard intent name (managed-repo,
	// managed-resource, managed-script, …) the controller uses to
	// reconcile this unit.
	Intent string `json:"intent"`
}

// BindingOrigin is advisory trace metadata placed on every ResolvedPackage
// that was materialized by a binding.
type BindingOrigin struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
	// Side is "provider" or "consumer".
	Side string `json:"side"`
}

type RenderedPaths struct {
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
}

type Placeholder struct {
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Sensitive bool   `json:"sensitive"`
	// Generator, when set, is the autogeneration recipe carried over
	// from the input that produced this placeholder. `gitups apply
	// --generate-secrets` reads it to fill the placeholder in place.
	// Render never consults this field.
	Generator *Generator `json:"generator,omitempty"`
}

// Generator declares an apply-time autogeneration recipe for a placeholder
// input. Closed set of kinds — see Generator* constants. Length is the
// produced string length in characters; ignored for kinds with a fixed
// length (uuid).
type Generator struct {
	Kind    string `json:"kind"`
	Length  int    `json:"length,omitempty"`
	Charset string `json:"charset,omitempty"`
}

// Generator kind constants. Adding a new kind requires updating the
// loader validation in internal/load and the generator implementation
// in internal/secrets.
const (
	GeneratorRandomHex    = "randomHex"
	GeneratorRandomBase64 = "randomBase64"
	GeneratorUUID         = "uuid"
)

// PackageDefinition is the on-disk package.yaml shape in the catalog. Render
// unit details live in install/*/descriptor.yaml and resources/*/descriptor.yaml.
type PackageDefinition struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Metadata   PackageMeta           `json:"metadata"`
	Spec       PackageDefinitionSpec `json:"spec"`
}

type PackageMeta struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type PackageDefinitionSpec struct {
	Role             Role          `json:"role"`
	Category         string        `json:"category,omitempty"`
	DefaultInstall   string        `json:"defaultInstall,omitempty"`
	DefaultResources []ResourceRef `json:"defaultResources,omitempty"`
	// Provides declares capabilities this package can fulfill as a provider
	// when bound to a consumer. Each entry names the resource template
	// instantiated on the provider side and the value pipe available to
	// consumers.
	Provides []CapabilityProvide `json:"provides,omitempty"`
	// Requires declares capabilities this package consumes. Each entry
	// names the resource template instantiated on the consumer side and
	// maps provider exports into that resource template's inputs.
	Requires []CapabilityRequire `json:"requires,omitempty"`
	// CLI describes how gitups invokes this controller's CLI during
	// bootstrap apply for units it owns. Required when Role is
	// RoleSRC; optional (and unused today) for RoleKRC. Forbidden for
	// workload packages.
	CLI *ControllerCLI `json:"cli,omitempty"`
	// Readiness lists the cluster-object conditions signalling this
	// package is done installing and ready to accept config. Required
	// for RoleKRC and RoleSRC (gitups apply gates handoff on these).
	// Optional on workloads — when present, downstream consumers can
	// reference the provider's readiness state.
	Readiness []ReadinessCheck `json:"readiness,omitempty"`
	// DeclarestBundle, when set, advertises that this package ships a
	// declarest metadata bundle. Advisory only — gitups never fetches
	// the bundle. Consumers composing declarest's ManagedService CR
	// read the bundle name+version to fill spec.metadata.bundle.
	DeclarestBundle *DeclarestBundle `json:"declarestBundle,omitempty"`
	// Compatibility expresses declared target-cluster constraints.
	// Fields are shape-only at load time; `gitups apply` probes the
	// target cluster and warns when a planned unit is outside its
	// package's declared range. Never consulted during render.
	Compatibility *Compatibility `json:"compatibility,omitempty"`
}

// Compatibility declares cluster-environment preconditions for a
// package. Every entry is a list of semver constraints (same grammar
// as Helm/go-mod: ">=1.29", "<1.35", "~1.30", "1.32.x", …) that a
// target cluster must satisfy. Empty list = no constraint.
type Compatibility struct {
	// Kubernetes constrains the target cluster's `kubectl version`
	// output. Empty = any version.
	Kubernetes []string `json:"kubernetes,omitempty"`
}

// DeclarestBundle advertises the declarest metadata bundle a package
// ships. Shape-only: gitups core does not fetch the bundle; declarest
// resolves it at runtime from ManagedService.spec.metadata.bundle.
type DeclarestBundle struct {
	// Name is the bundle identifier (e.g. "keycloak-bundle"). Used as
	// the left side of the <name>:<version> shorthand that goes into
	// ManagedService.spec.metadata.bundle.
	Name string `json:"name"`
	// Version is the bundle semver (without leading "v"). Used as the
	// right side of <name>:<version>.
	Version string `json:"version"`
	// Ref, when set, is the OCI artifact reference (e.g.
	// "ghcr.io/org/declarest-bundles/keycloak-bundle:1.0.0"). Advisory;
	// declarest resolves by <name>:<version> shorthand at runtime.
	Ref string `json:"ref,omitempty"`
}

// ControllerCLI describes how gitups invokes a controller's CLI during the
// bootstrap phase of gitups apply. Tokens in Args are rendered from a fixed
// template context: {{.KubeContext}}, {{.ManifestPath}}, {{.Namespace}}.
//
// Two invocation modes exist:
//
//   - CLI-only intents (e.g. `managed-script`): the SRC's CLI is the only
//     thing that touches the cluster for this unit. Gitups skips kubectl
//     apply entirely and runs the binary with `Args`.
//   - Bootstrap-sync intents (declared in `Intents`): gitups first kubectl
//     applies the rendered CR so an operator can later take over, then
//     invokes the binary with the per-intent `Args` so an immediate,
//     operator-independent bootstrap reconciliation runs on first install.
type ControllerCLI struct {
	Binary string   `json:"binary"`
	Args   []string `json:"args,omitempty"`
	// Intents maps a resource-template name to a per-intent CLI
	// invocation. A resource unit whose template name matches a key
	// here is tagged at resolve time as SRC-owned with that intent
	// and follows the bootstrap-sync flow at apply time.
	Intents map[string]ControllerCLIIntent `json:"intents,omitempty"`
}

// ControllerCLIIntent overrides the default CLI args for one named
// intent. Rendered against the same CLIContext as ControllerCLI.Args.
type ControllerCLIIntent struct {
	Args []string `json:"args"`
}

// CapabilityProvide declares one capability offered by a provider package.
type CapabilityProvide struct {
	Capability       string             `json:"capability"`
	ResourceTemplate string             `json:"resourceTemplate"`
	Exports          []CapabilityExport `json:"exports,omitempty"`
}

// CapabilityExport is one value a provider makes available to consumers.
// From selects the value source and follows a small grammar:
//   - "input:<key>"    read from the provider's resolved install values
//   - "binding:<key>"  read from the binding's Values map
//   - "placeholder"    emit __GITUPS_PLACEHOLDER__ on both sides
type CapabilityExport struct {
	Name   string `json:"name"`
	From   string `json:"from"`
	Secret bool   `json:"secret,omitempty"`
}

// CapabilityRequire declares one capability consumed by a consumer package.
type CapabilityRequire struct {
	Capability       string              `json:"capability"`
	ResourceTemplate string              `json:"resourceTemplate"`
	Consumes         []CapabilityConsume `json:"consumes,omitempty"`
}

// CapabilityConsume maps a provider export onto an input of the consumer's
// resource template. Input is the descriptor input key (may be dotted);
// From is "provider.<exportName>".
type CapabilityConsume struct {
	Input string `json:"input"`
	From  string `json:"from"`
}

// PackageDescriptor is the on-disk descriptor.yaml shape for an install method
// or resource template.
type PackageDescriptor struct {
	APIVersion string                `json:"apiVersion"`
	Kind       string                `json:"kind"`
	Metadata   ObjectMeta            `json:"metadata,omitempty"`
	Spec       PackageDescriptorSpec `json:"spec"`
}

type PackageDescriptorSpec struct {
	Renderer  string         `json:"renderer"`
	Inputs    []InputSpec    `json:"inputs,omitempty"`
	DependsOn []string       `json:"dependsOn,omitempty"`
	Hooks     *HookSpec      `json:"hooks,omitempty"`
	Overlays  []string       `json:"overlays,omitempty"`
	OLM       *OLMSpec       `json:"olm,omitempty"`
	Helm      *HelmSpec      `json:"helm,omitempty"`
	Kustomize *KustomizeSpec `json:"kustomize,omitempty"`
	// ProvisioningStyle tells gitups how the resource template creates its
	// cluster object. Values: "crd" (default) emits a custom resource via
	// the chosen renderer; "script" ships scripts/provision.sh (and
	// optional scripts/teardown.sh) that gitups wraps in a deterministic
	// Job manifest at render time.
	ProvisioningStyle string `json:"provisioningStyle,omitempty"`
	// ScriptImage is the container image gitups runs the provisioning
	// script in. Required when ProvisioningStyle is "script"; must be
	// pinned (tag or digest), never "latest". Gitups owns the Job shape
	// around it.
	ScriptImage string `json:"scriptImage,omitempty"`
	// ScriptServiceAccount optionally names a ServiceAccount for the
	// generated Job. Empty defaults to "default". Packages that need
	// elevated RBAC author a ServiceAccount + Role/RoleBinding alongside
	// their descriptor and reference it here.
	ScriptServiceAccount string `json:"scriptServiceAccount,omitempty"`
	// Readiness lists cluster objects that must reach the named condition
	// before units depending on this one are applied. Static; never read
	// from the cluster during render. Template fields may reference
	// .Values.
	Readiness []ReadinessCheck `json:"readiness,omitempty"`
}

// ReadinessCheck references a Kubernetes object that must satisfy the named
// Condition type before dependent units apply. Used to compute apply waves
// and emit `gitups.io/readiness` annotations.
type ReadinessCheck struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Condition string `json:"condition"`
}

// ProvisioningStyle constants.
const (
	ProvisioningStyleCRD    = "crd"
	ProvisioningStyleScript = "script"
)

// ApplyWaveAnnotation is the controller-agnostic sync-wave annotation
// emitted on every rendered manifest. Controller packages translate this
// into their native sync-wave key via an overlay.
const ApplyWaveAnnotation = "gitups.io/apply-wave"

// ReadinessAnnotation records the readiness checks (JSON-encoded) a
// rendered object relies on. Advisory — consumed by `gitups wait` and any
// controller overlay that wants to emit native health checks.
const ReadinessAnnotation = "gitups.io/readiness"

type OLMSpec struct {
	Package             string `json:"package"`
	Channel             string `json:"channel"`
	Source              string `json:"source"`
	SourceNamespace     string `json:"sourceNamespace"`
	StartingCSV         string `json:"startingCSV"`
	InstallPlanApproval string `json:"installPlanApproval,omitempty"`
	// OperatorGroupScope picks the OperatorGroup target-namespace mode:
	//   AllNamespaces    → empty spec (cluster-wide CSV propagation)
	//   OwnNamespace     → spec.targetNamespaces = [<namespace>] (default)
	//   SingleNamespace  → spec.targetNamespaces = [<namespace>] today
	// Override per instance via resolvedValues.olm.operatorGroupScope.
	OperatorGroupScope string `json:"operatorGroupScope,omitempty"`
}

type HelmSpec struct {
	Repo           string `json:"repo"`
	Chart          string `json:"chart"`
	Version        string `json:"version"`
	ValuesTemplate string `json:"valuesTemplate,omitempty"`
}

type KustomizeSpec struct {
	Base           string `json:"base"`
	ValuesTemplate string `json:"valuesTemplate,omitempty"`
}

type InputSpec struct {
	Name              string     `json:"name"`
	Type              string     `json:"type"`
	Default           any        `json:"default,omitempty"`
	Enum              []any      `json:"enum,omitempty"`
	Required          bool       `json:"required,omitempty"`
	Sensitive         bool       `json:"sensitive,omitempty"`
	Placeholder       bool       `json:"placeholder,omitempty"`
	PlaceholderReason string     `json:"placeholderReason,omitempty"`
	Generator         *Generator `json:"generator,omitempty"`
}

type HookSpec struct {
	PreRender  string `json:"preRender,omitempty"`
	PostRender string `json:"postRender,omitempty"`
}

// TypeMeta is the minimal shape needed to detect what kind of document a YAML
// file contains before full unmarshal.
type TypeMeta struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
}
