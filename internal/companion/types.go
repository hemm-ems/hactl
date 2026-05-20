package companion

// HealthResponse is the response from GET /v1/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// StatusResponse is the response from GET /v1/status.
type StatusResponse struct {
	Version            string `json:"version"`
	SupervisorReachable bool   `json:"supervisor_reachable"`
	HasHACLI           bool   `json:"has_ha_cli"`
	ConfigWritable     bool   `json:"config_writable"`
	IngressActive      bool   `json:"ingress_active"`
	AuthMode           string `json:"auth_mode"`
}

// ConfigFilesResponse is the response from GET /v1/config/files.
type ConfigFilesResponse struct {
	Files []string `json:"files"`
}

// ConfigFileResponse is the response from GET /v1/config/file.
type ConfigFileResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ConfigBlockResponse is the response from GET /v1/config/block.
type ConfigBlockResponse struct {
	Path    string `json:"path"`
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ConfigWriteResponse is the response from PUT /v1/config/file.
type ConfigWriteResponse struct {
	Status string `json:"status"`
	Diff   string `json:"diff,omitempty"`
	Backup string `json:"backup,omitempty"`
}

// TemplateDefinition represents a template sensor definition.
type TemplateDefinition struct {
	UniqueID          string `json:"unique_id"`
	Name              string `json:"name"`
	Domain            string `json:"domain"`
	State             string `json:"state"`
	UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
	DeviceClass       string `json:"device_class,omitempty"`
}

// TemplatesResponse is the response from GET /v1/config/templates.
type TemplatesResponse struct {
	Templates []TemplateDefinition `json:"templates"`
}

// TemplateResponse is the response from GET /v1/config/template.
type TemplateResponse struct {
	UniqueID string `json:"unique_id"`
	Content  string `json:"content"`
}

// TemplateCreateResponse is the response from POST /v1/config/template.
type TemplateCreateResponse struct {
	Status   string `json:"status"`
	UniqueID string `json:"unique_id"`
}

// ScriptDefinition represents a script definition.
type ScriptDefinition struct {
	ID     string `json:"id"`
	Alias  string `json:"alias"`
	Mode   string `json:"mode"`
	Fields []any  `json:"fields,omitempty"`
}

// ScriptsResponse is the response from GET /v1/config/scripts.
type ScriptsResponse struct {
	Scripts []ScriptDefinition `json:"scripts"`
}

// ScriptResponse is the response from GET /v1/config/script.
type ScriptResponse struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ScriptCreateResponse is the response from POST /v1/config/script.
type ScriptCreateResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

// AutomationDefinition represents an automation definition.
type AutomationDefinition struct {
	ID          string `json:"id"`
	Alias       string `json:"alias"`
	Mode        string `json:"mode,omitempty"`
	Description string `json:"description,omitempty"`
}

// AutomationsResponse is the response from GET /v1/config/automations.
type AutomationsResponse struct {
	Automations []AutomationDefinition `json:"automations"`
}

// AutomationResponse is the response from GET /v1/config/automation.
type AutomationResponse struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// AutomationCreateResponse is the response from POST /v1/config/automation.
type AutomationCreateResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}

// ConfigDeleteResponse is the response from DELETE endpoints.
type ConfigDeleteResponse struct {
	Status string `json:"status"`
}

// HelperDefinition represents a helper entity definition.
type HelperDefinition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Icon   string `json:"icon,omitempty"`
}

// HelpersResponse is the response from GET /v1/config/helpers.
type HelpersResponse struct {
	Helpers []HelperDefinition `json:"helpers"`
}

// HelperResponse is the response from GET /v1/config/helper.
type HelperResponse struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Content string `json:"content"`
}

// HelperCreateResponse is the response from POST /v1/config/helper.
type HelperCreateResponse struct {
	Status string `json:"status"`
	ID     string `json:"id"`
}
