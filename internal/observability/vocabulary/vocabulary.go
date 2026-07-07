// Package vocabulary is the compile-time contract for structured logging.
//
// Every log emission must specify a Module (which package/subsystem is
// speaking) and an Action (what specific event is being reported). Both
// are typed enums declared here; the logging.Emit function accepts only
// these types, so a typo becomes a compile error rather than an
// unindexable string in Loki.
//
// New (module, action) pairs are one-line additions: declare the const
// here (or in the appropriate actions_<family>.go file) and it's
// available everywhere.
//
// See docs/rebuild-plan.md §11 for the full vocabulary spec.
package vocabulary

// Module identifies the emitting subsystem. Every log line and every
// derived metric carries a Module label.
type Module string

// All Module values known to the vocabulary. Adding a Module here makes
// it available to logging.Emit; ValidModules is the runtime-verifiable
// registry used at startup to catch typos.
const (
	// ── Workflows (Temporal orchestration layer) ──
	ModuleIngestWorkflow    Module = "ingest_workflow"
	ModuleMonitorWorkflow   Module = "monitor_workflow"
	ModuleDiscoveryWorkflow Module = "discovery_workflow"
	ModuleDownloadWorkflow  Module = "download_workflow"
	ModuleUploadWorkflow    Module = "upload_workflow"

	// ── Domain packages (internal/domain/...) ──
	ModuleFixture      Module = "fixture"
	ModuleEvent        Module = "event"
	ModuleVideo        Module = "video"
	ModuleAlias        Module = "alias"
	ModuleDiscovery    Module = "discovery"
	ModuleVision       Module = "vision"
	ModuleSession      Module = "session"
	ModuleTextAnalysis Module = "textanalysis"

	// ── Infrastructure adapters (internal/infra/...) ──
	ModuleInfraPG          Module = "infra_pg"
	ModuleInfraNATS        Module = "infra_nats"
	ModuleInfraEvent       Module = "infra_event"
	ModuleInfraS3          Module = "infra_s3"
	ModuleInfraLLM         Module = "infra_llm"
	ModuleInfraTemporal    Module = "infra_temporal"
	ModuleInfraAPIFootball Module = "infra_apifootball"
	ModuleInfraTwitter     Module = "infra_twitter"
	ModuleInfraSyndication Module = "infra_syndication"
	ModuleInfraFFmpeg      Module = "infra_ffmpeg"
	ModuleInfraWikidata    Module = "infra_wikidata"

	// ── Cross-cutting subsystems ──
	ModuleAPI             Module = "api"
	ModuleAPISSE          Module = "api_sse"
	ModuleWebhookDelivery Module = "webhook_delivery"
	ModuleScaler          Module = "scaler"
	ModuleWorker          Module = "worker"          // binary lifecycle
	ModuleAPIServer       Module = "api_server"      // binary lifecycle
	ModuleTwitterService  Module = "twitter_service" // binary lifecycle
	ModuleMigration       Module = "migration"
	ModuleHealthz         Module = "healthz"
	ModuleDeploy          Module = "deploy" // startup + shutdown markers
)

// ValidModules is the runtime-verifiable list of every declared Module.
// The logging package's initializer walks this to build an in-memory set
// used to catch typos in tests + to feed the vocabulary catalog
// generator per §15.3.
var ValidModules = []Module{
	ModuleIngestWorkflow, ModuleMonitorWorkflow, ModuleDiscoveryWorkflow,
	ModuleDownloadWorkflow, ModuleUploadWorkflow,

	ModuleFixture, ModuleEvent, ModuleVideo, ModuleAlias,
	ModuleDiscovery, ModuleVision, ModuleSession, ModuleTextAnalysis,

	ModuleInfraPG, ModuleInfraNATS, ModuleInfraEvent, ModuleInfraS3,
	ModuleInfraLLM, ModuleInfraTemporal, ModuleInfraAPIFootball,
	ModuleInfraTwitter, ModuleInfraSyndication, ModuleInfraFFmpeg,
	ModuleInfraWikidata,

	ModuleAPI, ModuleAPISSE, ModuleWebhookDelivery, ModuleScaler,
	ModuleWorker, ModuleAPIServer, ModuleTwitterService,
	ModuleMigration, ModuleHealthz, ModuleDeploy,
}

// Action identifies the specific event being reported. Actions are
// declared per-family in actions_<family>.go files so they cluster
// with the module they belong to. See actions_deploy.go for the first
// concrete family.
type Action string

// ValidActions is populated by each actions_<family>.go file via an
// init() function. The logging package walks it at startup for the
// same reasons as ValidModules.
var ValidActions = []Action{}

// registerActions is called by each per-family actions file's init() to
// append its declared actions to the global ValidActions list.
func registerActions(actions ...Action) {
	ValidActions = append(ValidActions, actions...)
}
