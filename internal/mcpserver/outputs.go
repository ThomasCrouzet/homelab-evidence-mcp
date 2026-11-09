package mcpserver

import (
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/evidence"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/httpx"
	"github.com/ThomasCrouzet/homelab-evidence-mcp/internal/registry"
)

// AdapterRow gives one configured adapter in capabilities output.
type AdapterRow struct {
	Name string              `json:"name"`
	Kind evidence.SourceKind `json:"kind"`
}

// CapabilitiesOut is the output of evidence_capabilities.
type CapabilitiesOut struct {
	Name               string                `json:"name"`
	Version            string                `json:"version"`
	ReadOnly           bool                  `json:"readonly"`
	Transport          string                `json:"transport"`
	AdaptersActive     []AdapterRow          `json:"adapters_active"`
	AdapterKinds       []evidence.SourceKind `json:"adapter_kinds"`
	ServicesCount      int                   `json:"services_count"`
	Tools              []string              `json:"tools"`
	Limits             map[string]any        `json:"limits"`
	SourceCache        httpx.CacheStats      `json:"source_cache"`
	EvidenceCache      evidence.CacheStats   `json:"evidence_cache"`
	Features           []string              `json:"features"`
	CompatibilityNotes []string              `json:"compatibility_notes"`
	Warnings           []string              `json:"warnings"`
}

// ListServicesOut is the output of list_services.
type ListServicesOut struct {
	Services []registry.ServiceView `json:"services"`
	Total    int                    `json:"total"`
	Offset   int                    `json:"offset"`
	Limit    int                    `json:"limit"`
}

// ServiceStatusOut is the output of service_status.
type ServiceStatusOut struct {
	ServiceID           string                   `json:"service_id"`
	DisplayName         string                   `json:"display_name"`
	Items               []evidence.Item          `json:"items"`
	Sources             []evidence.SourceOutcome `json:"sources"`
	Complete            bool                     `json:"complete"`
	SourceComplete      bool                     `json:"source_complete"`
	DeliveryComplete    bool                     `json:"delivery_complete"`
	Truncated           bool                     `json:"truncated"`
	ByteBudget          int                      `json:"byte_budget"`
	ByteSize            int                      `json:"byte_size"`
	ByteBudgetTruncated bool                     `json:"byte_budget_truncated"`
	OmittedItems        int                      `json:"omitted_items"`
	RetrievedAt         string                   `json:"retrieved_at"`
	Note                string                   `json:"note"`
}

// IncidentContextOut is the output of incident_context.
type IncidentContextOut evidence.Bundle

// SearchLogsOut is the output of search_logs.
type SearchLogsOut struct {
	ServiceID           string                 `json:"service_id"`
	EffectiveStart      string                 `json:"effective_start"`
	EffectiveEnd        string                 `json:"effective_end"`
	Items               []evidence.Item        `json:"items"`
	Source              evidence.SourceOutcome `json:"source"`
	Complete            bool                   `json:"complete"`
	SourceComplete      bool                   `json:"source_complete"`
	DeliveryComplete    bool                   `json:"delivery_complete"`
	ByteBudget          int                    `json:"byte_budget"`
	ByteSize            int                    `json:"byte_size"`
	ByteBudgetTruncated bool                   `json:"byte_budget_truncated"`
	OmittedItems        int                    `json:"omitted_items"`
	Note                string                 `json:"note"`
}

// FailedCronsOut is the output of failed_crons.
type FailedCronsOut struct {
	EffectiveStart      string                   `json:"effective_start"`
	EffectiveEnd        string                   `json:"effective_end"`
	Items               []evidence.Item          `json:"items"`
	Sources             []evidence.SourceOutcome `json:"sources"`
	Complete            bool                     `json:"complete"`
	SourceComplete      bool                     `json:"source_complete"`
	DeliveryComplete    bool                     `json:"delivery_complete"`
	Truncated           bool                     `json:"truncated"`
	TimeBasis           string                   `json:"time_basis"`
	ByteBudget          int                      `json:"byte_budget"`
	ByteSize            int                      `json:"byte_size"`
	ByteBudgetTruncated bool                     `json:"byte_budget_truncated"`
	OmittedItems        int                      `json:"omitted_items"`
	Note                string                   `json:"note"`
}

// GetEvidenceOut is the output of get_evidence.
type GetEvidenceOut struct {
	Item       evidence.Item `json:"item"`
	ByteBudget int           `json:"byte_budget"`
	ByteSize   int           `json:"byte_size"`
}

var (
	capabilitiesSchema    = mustOutputSchema[CapabilitiesOut]()
	listServicesSchema    = mustOutputSchema[ListServicesOut]()
	serviceStatusSchema   = mustOutputSchema[ServiceStatusOut]()
	incidentContextSchema = mustOutputSchema[IncidentContextOut]()
	searchLogsSchema      = mustOutputSchema[SearchLogsOut]()
	failedCronsSchema     = mustOutputSchema[FailedCronsOut]()
	getEvidenceSchema     = mustOutputSchema[GetEvidenceOut]()
)

func mustOutputSchema[T any]() *jsonschema.Schema {
	options := &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[evidence.Kind]():       {Type: "string", Enum: evidenceKindEnum()},
		reflect.TypeFor[evidence.SourceKind](): {Type: "string", Enum: sourceKindEnum()},
		reflect.TypeFor[evidence.Severity]():   {Type: "string", Enum: severityEnum()},
		reflect.TypeFor[evidence.Freshness]():  {Type: "string", Enum: freshnessEnum()},
	}}
	schema, err := jsonschema.For[T](options)
	if err != nil {
		panic("output schema: " + err.Error())
	}
	applyStatusEnum(schema)
	return schema
}

// applyStatusEnum marks the source outcome status as a closed vocabulary.
func applyStatusEnum(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	for name, property := range schema.Properties {
		switch name {
		case "status":
			property.Enum = stringEnum("ok", "error", "skipped", "timeout", "absent", "canceled")
		case "reason":
			property.Enum = stringEnum("", "empty_result", "no_binding", "unmatched_binding",
				"unsupported_history", "partial_invalid_records", "truncated",
				"timeout", "canceled", "upstream_error", "authentication_failed",
				"not_queried_by_tool", "unknown_history_coverage", "partial_fetch",
				"missing_identity", "omitted_checks", "ambiguous_binding",
				"outside_registry_scope", "retention_limited",
				"status_filter_no_match", "identity_unverified", "request_budget_exceeded")
		case "cache_state":
			property.Enum = stringEnum("", "hit", "miss", "mixed", "coalesced", "disabled")
		}
		applyStatusEnum(property)
	}
	applyStatusEnum(schema.Items)
	for _, definition := range schema.Defs {
		applyStatusEnum(definition)
	}
	for _, definition := range schema.Definitions {
		applyStatusEnum(definition)
	}
}

func sourceKindEnum() []any {
	return stringEnum(string(evidence.SourceGatus), string(evidence.SourceDocker),
		string(evidence.SourceLoki), string(evidence.SourceHealthchecks),
		string(evidence.SourceBeszel), string(evidence.SourceNtfy))
}

func evidenceKindEnum() []any {
	return stringEnum(string(evidence.KindEndpointCheck),
		string(evidence.KindContainer), string(evidence.KindLogLine),
		string(evidence.KindCronCheck), string(evidence.KindMetricSample),
		string(evidence.KindNotification))
}

func severityEnum() []any {
	return stringEnum(string(evidence.SeverityInfo), string(evidence.SeverityWarning),
		string(evidence.SeverityError), string(evidence.SeverityCritical),
		string(evidence.SeverityUnknown))
}

func freshnessEnum() []any {
	return stringEnum(string(evidence.FreshnessLive), string(evidence.FreshnessRecent),
		string(evidence.FreshnessStale), string(evidence.FreshnessUnknown),
		string(evidence.FreshnessCached), string(evidence.FreshnessMissing))
}

func stringEnum(values ...string) []any {
	enum := make([]any, len(values))
	for index, value := range values {
		enum[index] = value
	}
	return enum
}
