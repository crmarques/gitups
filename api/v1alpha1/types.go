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
	EnvKey  string          `json:"envKey,omitempty"`
	Extends *Extends        `json:"extends,omitempty"`
	Sources []PackageSource `json:"sources"`
	// Repositories enumerates output repos. Generic repos select package
	// installs; env repos select or derive package resources.
	Repositories []RepositoryDecl `json:"repositories,omitempty"`
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
	Role          string         `json:"role,omitempty"`
	InstallMethod string         `json:"installMethod,omitempty"`
	Values        map[string]any `json:"values,omitempty"`
	Resources     []ResourceRef  `json:"resources,omitempty"`
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
	PackageInstance  string         `json:"packageInstance,omitempty"`
	UnitType         string         `json:"unitType"`
	InstallMethod    string         `json:"installMethod,omitempty"`
	ResourceTemplate string         `json:"resourceTemplate,omitempty"`
	ResourceName     string         `json:"resourceName,omitempty"`
	Repository       string         `json:"repository"`
	Instance         string         `json:"instance"`
	Role             string         `json:"role"`
	Renderer         string         `json:"renderer"`
	DependsOn        []string       `json:"dependsOn,omitempty"`
	ResolvedValues   map[string]any `json:"resolvedValues"`
	RenderedPaths    RenderedPaths  `json:"renderedPaths"`
}

type RenderedPaths struct {
	Repo string `json:"repo"`
	Dir  string `json:"dir"`
}

type Placeholder struct {
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Sensitive bool   `json:"sensitive"`
}

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
	Role             string        `json:"role"`
	Category         string        `json:"category,omitempty"`
	DefaultInstall   string        `json:"defaultInstall,omitempty"`
	DefaultResources []ResourceRef `json:"defaultResources,omitempty"`
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
}

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
	Name              string `json:"name"`
	Type              string `json:"type"`
	Default           any    `json:"default,omitempty"`
	Enum              []any  `json:"enum,omitempty"`
	Required          bool   `json:"required,omitempty"`
	Sensitive         bool   `json:"sensitive,omitempty"`
	Placeholder       bool   `json:"placeholder,omitempty"`
	PlaceholderReason string `json:"placeholderReason,omitempty"`
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
