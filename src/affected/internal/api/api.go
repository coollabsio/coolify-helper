// Package api is the JSON contract between Coolify and coolify-affected
// (schema version 1). The JSON Schemas in schema/ describe the same documents;
// a test keeps them in sync.
package api

import (
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

// SchemaVersion is the version of the JSON contract. Bump it on any breaking change.
const SchemaVersion = 1

// Exit codes.
const (
	ExitOK      = 0
	ExitInvalid = 2
	ExitFatal   = 3
)

// Decisions.
const (
	Deploy = "deploy"
	Skip   = "skip"
)

// Reason codes.
const (
	ReasonMatched           = "matched"
	ReasonWatchPathsMatched = "watch_paths_matched"
	ReasonNoMatch           = "no_match"
	ReasonWatchPathsNoMatch = "watch_paths_no_match"
	ReasonNoBase            = "no_base"
	ReasonBaseUnreachable   = "base_unreachable"
	ReasonEmptyDiff         = "empty_diff"
	ReasonTooManyChanges    = "too_many_changes"
	ReasonForkNoNarrowing   = "fork_no_narrowing"
	ReasonResolutionFailed  = "resolution_failed"
	ReasonTimeout           = "timeout"
	ReasonError             = "error"
)

// Diff statuses.
const (
	DiffOK              = "ok"
	DiffBaseUnreachable = "base_unreachable"
	DiffTooManyChanges  = "too_many_changes"
	DiffError           = "error"
)

// Relations between base and after.
const (
	RelationAncestor  = "ancestor"
	RelationIdentical = "identical"
	RelationDiverged  = "diverged"
	RelationUnrelated = "unrelated"
)

// Modes and build packs.
const (
	ModeSmart      = "smart"
	ModeWatchPaths = "watch_paths"

	BuildPackDockerfile    = "dockerfile"
	BuildPackDockerCompose = "dockercompose"
	BuildPackRailpack      = "railpack"
	BuildPackNixpacks      = "nixpacks"
	BuildPackStatic        = "static"
)

// Repository is where the commits come from.
type Repository struct {
	URL        string            `json:"url"`
	MirrorKey  string            `json:"mirror_key"`
	GitEnv     map[string]string `json:"git_env,omitempty"`
	GitConfig  []string          `json:"git_config,omitempty"`
	Submodules bool              `json:"submodules,omitempty"`
}

// Commands are the application's commands, used to find the package it builds.
type Commands struct {
	Install      *string `json:"install"`
	Build        *string `json:"build"`
	Start        *string `json:"start"`
	ComposeBuild *string `json:"compose_build"`
	ComposeStart *string `json:"compose_start"`
}

// List returns the commands that are set.
func (c Commands) List() []string {
	var out []string
	for _, s := range []*string{c.Install, c.Build, c.Start, c.ComposeBuild, c.ComposeStart} {
		if s != nil && *s != "" {
			out = append(out, *s)
		}
	}
	return out
}

// Application is one candidate application.
type Application struct {
	ID                    string   `json:"id"`
	BaseCommit            *string  `json:"base_commit"`
	MergeBaseRef          *string  `json:"merge_base_ref"`
	Mode                  string   `json:"mode"`
	WatchPaths            *string  `json:"watch_paths"`
	BuildPack             string   `json:"build_pack"`
	BaseDirectory         string   `json:"base_directory"`
	DockerfileLocation    *string  `json:"dockerfile_location"`
	DockerComposeLocation *string  `json:"docker_compose_location"`
	Commands              Commands `json:"commands"`
	ReferencePaths        []string `json:"reference_paths"`
	ManagedConfigPaths    []string `json:"managed_config_paths"`
}

// Limits of one evaluate run.
type Limits struct {
	TimeoutSeconds   int   `json:"timeout_seconds"`
	MaxChangedFiles  int   `json:"max_changed_files"`
	MaxManifestFiles int   `json:"max_manifest_files"`
	MaxManifestBytes int64 `json:"max_manifest_bytes"`
	MaxMirrorBytes   int64 `json:"max_mirror_bytes"`
	MaxReportedFiles int   `json:"max_reported_files"`
}

// DefaultLimits are used for every limit the request leaves out (or sets to 0).
var DefaultLimits = Limits{
	TimeoutSeconds:   120,
	MaxChangedFiles:  50000,
	MaxManifestFiles: 5000,
	MaxManifestBytes: 50 << 20,
	MaxMirrorBytes:   10 << 30,
	MaxReportedFiles: 200,
}

// EvaluateRequest is the input of `evaluate`.
type EvaluateRequest struct {
	SchemaVersion int           `json:"schema_version"`
	Repository    Repository    `json:"repository"`
	After         string        `json:"after"`
	FetchRefs     []string      `json:"fetch_refs"`
	IsFork        bool          `json:"is_fork"`
	Applications  []Application `json:"applications"`
	Limits        Limits        `json:"limits"`
}

// Diff is the tree diff between one base and after.
type Diff struct {
	Status                string   `json:"status"`
	Relation              string   `json:"relation,omitempty"`
	ChangedFilesCount     int      `json:"changed_files_count"`
	ChangedFilesTruncated bool     `json:"changed_files_truncated"`
	ChangedFiles          []string `json:"changed_files"`
	// Error is a sanitized message, for status error.
	Error string `json:"error,omitempty"`
}

// Match explains why an application deploys.
type Match struct {
	File string   `json:"file"`
	Rule string   `json:"rule"`
	Via  []string `json:"via,omitempty"`
}

// Lockfile reports the lockfiles that changed.
type Lockfile struct {
	Files      []string `json:"files"`
	AffectsApp bool     `json:"affects_app"`
	Detail     string   `json:"detail"`
}

// Anchor is the package an application builds.
type Anchor struct {
	Resolver string `json:"resolver"`
	Package  string `json:"package"`
	Dir      string `json:"dir"`
	Source   string `json:"source"`
}

// TriggerSet is the resolved set of an application.
type TriggerSet struct {
	Compiled  match.Set     `json:"compiled"`
	Display   []string      `json:"display"`
	Limits    []string      `json:"limits"`
	Resolvers []string      `json:"resolvers"`
	Anchor    *Anchor       `json:"anchor"`
	Anchors   []Anchor      `json:"anchors"`
	Widened   []graph.Widen `json:"widened"`
	// ManifestFingerprint is sha256 over the sorted (path, blob id, mode) of every
	// file the resolution read.
	ManifestFingerprint string `json:"manifest_fingerprint"`
}

// AppResult is the decision for one application.
type AppResult struct {
	ID         string      `json:"id"`
	Decision   string      `json:"decision"`
	ReasonCode string      `json:"reason_code"`
	Reason     string      `json:"reason"`
	Base       *string     `json:"base"`
	Matches    []Match     `json:"matches"`
	Lockfile   *Lockfile   `json:"lockfile,omitempty"`
	TriggerSet *TriggerSet `json:"trigger_set,omitempty"`
}

// EvaluateResponse is the output of `evaluate`.
type EvaluateResponse struct {
	SchemaVersion int              `json:"schema_version"`
	ToolVersion   string           `json:"tool_version"`
	After         string           `json:"after"`
	TimingsMS     map[string]int64 `json:"timings_ms"`
	Diffs         map[string]*Diff `json:"diffs"`
	Applications  []AppResult      `json:"applications"`
	Errors        []string         `json:"errors"`
}

// ScanLimits are the limits of one scan.
type ScanLimits struct {
	TimeoutSeconds int   `json:"timeout_seconds"`
	MaxFiles       int   `json:"max_files"`
	MaxFileBytes   int64 `json:"max_file_bytes"`
}

// DefaultScanLimits are used for every scan limit left out.
var DefaultScanLimits = ScanLimits{TimeoutSeconds: 60, MaxFiles: 100000, MaxFileBytes: 1 << 20}

// ScanRequest is the input of `scan`.
type ScanRequest struct {
	SchemaVersion int         `json:"schema_version"`
	Worktree      string      `json:"worktree"`
	Commit        string      `json:"commit"`
	Application   Application `json:"application"`
	Limits        ScanLimits  `json:"limits"`
}

// Evidence is one reference scan found.
type Evidence struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ScanResponse is the output of `scan`.
type ScanResponse struct {
	SchemaVersion  int              `json:"schema_version"`
	ToolVersion    string           `json:"tool_version"`
	Commit         string           `json:"commit"`
	ReferencePaths []string         `json:"reference_paths"`
	Evidence       []Evidence       `json:"evidence"`
	Widened        []graph.Widen    `json:"widened"`
	TimingsMS      map[string]int64 `json:"timings_ms"`
}

// ResolveRequest is the input of `resolve`: a worktree, or a commit of a mirror.
type ResolveRequest struct {
	SchemaVersion int           `json:"schema_version"`
	Worktree      string        `json:"worktree,omitempty"`
	MirrorKey     string        `json:"mirror_key,omitempty"`
	Commit        string        `json:"commit,omitempty"`
	Applications  []Application `json:"applications"`
	Limits        Limits        `json:"limits"`
}

// ResolvedApp is one application of a resolve response.
type ResolvedApp struct {
	ID         string     `json:"id"`
	TriggerSet TriggerSet `json:"trigger_set"`
}

// ResolveResponse is the output of `resolve`.
type ResolveResponse struct {
	SchemaVersion int              `json:"schema_version"`
	ToolVersion   string           `json:"tool_version"`
	Commit        string           `json:"commit"`
	Graph         *graph.Graph     `json:"graph"`
	Applications  []ResolvedApp    `json:"applications"`
	Errors        []string         `json:"errors"`
	TimingsMS     map[string]int64 `json:"timings_ms"`
}

// PrunedMirror is one mirror `mirror prune` removed.
type PrunedMirror struct {
	MirrorKey string `json:"mirror_key"`
	Bytes     int64  `json:"bytes"`
	Reason    string `json:"reason"`
}

// PruneResponse is the output of `mirror prune`.
type PruneResponse struct {
	SchemaVersion int            `json:"schema_version"`
	ToolVersion   string         `json:"tool_version"`
	Removed       []PrunedMirror `json:"removed"`
	Kept          int            `json:"kept"`
	TotalBytes    int64          `json:"total_bytes"`
	Errors        []string       `json:"errors"`
}

// VersionResponse is the output of `version --json`.
type VersionResponse struct {
	Version       string `json:"version"`
	SchemaVersion int    `json:"schema_version"`
}
