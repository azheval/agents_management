package web

import (
	"agent-management/server/internal/auth"
	"agent-management/server/internal/storage"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type ExecPolicyParameterDefinition struct {
	Name          string   `json:"name"`
	Label         string   `json:"label"`
	Type          string   `json:"type"`
	Required      bool     `json:"required"`
	Editable      bool     `json:"editable"`
	Secret        bool     `json:"secret"`
	DefaultValue  string   `json:"default_value"`
	ValidationRE  string   `json:"validation_regex"`
	AllowedValues []string `json:"allowed_values"`
}

type ExecPolicyView struct {
	ID          string                          `json:"id"`
	Name        string                          `json:"name"`
	Description string                          `json:"description"`
	Parameters  []ExecPolicyParameterDefinition `json:"parameters"`
}

type ExecPolicyResolvedTask struct {
	PolicyID  uuid.UUID
	BindingID uuid.UUID
	Command   string
	Args      []string
}

type ExecPolicyCard struct {
	Policy           *storage.ExecCommandPolicy
	VisibleCommand   string
	VisibleArgs      []string
	CanViewDetails   bool
	ParsedParameters []ExecPolicyParameterDefinition
	Bindings         []*ExecPolicyBindingCard
}

type ExecPolicyBindingCard struct {
	Binding          *storage.ExecCommandPolicyBinding
	AgentName        string
	VisibleCommand   string
	VisibleArgs      []string
	VisibleParamJSON string
}

type ExecPolicyPreset struct {
	Key             string                          `json:"key"`
	Name            string                          `json:"name"`
	Description     string                          `json:"description"`
	CommandTemplate string                          `json:"command_template"`
	ArgsTemplate    []string                        `json:"args_template"`
	ParameterSchema []ExecPolicyParameterDefinition `json:"parameter_schema"`
}

type ExecPolicyExportBundle struct {
	Version  int                      `json:"version"`
	Policies []ExecPolicyExportPolicy `json:"policies"`
}

type ExecPolicyExportPolicy struct {
	Name            string                          `json:"name"`
	Description     string                          `json:"description"`
	CommandTemplate string                          `json:"command_template"`
	ArgsTemplate    []string                        `json:"args_template"`
	ParameterSchema []ExecPolicyParameterDefinition `json:"parameter_schema"`
	IsActive        bool                            `json:"is_active"`
	Bindings        []ExecPolicyExportBinding       `json:"bindings"`
}

type ExecPolicyExportBinding struct {
	AgentID                 string            `json:"agent_id"`
	AgentHostname           string            `json:"agent_hostname"`
	CommandTemplateOverride string            `json:"command_template_override,omitempty"`
	ArgsTemplateOverride    []string          `json:"args_template_override,omitempty"`
	ParameterValues         map[string]string `json:"parameter_values"`
	IsActive                bool              `json:"is_active"`
}

func parseExecPolicyParameterSchema(raw []byte) ([]ExecPolicyParameterDefinition, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var defs []ExecPolicyParameterDefinition
	if err := json.Unmarshal(raw, &defs); err != nil {
		return nil, err
	}
	for i := range defs {
		defs[i].Name = strings.TrimSpace(defs[i].Name)
		defs[i].Label = strings.TrimSpace(defs[i].Label)
		defs[i].Type = strings.TrimSpace(defs[i].Type)
		if defs[i].Type == "" {
			defs[i].Type = "text"
		}
		if defs[i].Label == "" {
			defs[i].Label = defs[i].Name
		}
	}
	return defs, nil
}

func parseJSONStringMap(raw []byte) (map[string]string, error) {
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		parsed = map[string]string{}
	}
	return parsed, nil
}

func collectExecPolicyUserValues(form map[string][]string) map[string]string {
	values := make(map[string]string)
	const prefix = "exec_policy_param_"
	for key, raw := range form {
		if !strings.HasPrefix(key, prefix) || len(raw) == 0 {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(key, prefix))
		if name == "" {
			continue
		}
		values[name] = raw[0]
	}
	return values
}

func interpolateExecTemplate(input string, values map[string]string) string {
	result := input
	for key, value := range values {
		result = strings.ReplaceAll(result, "{{"+key+"}}", value)
	}
	return result
}

func validateExecPolicyValues(defs []ExecPolicyParameterDefinition, values map[string]string, userInputOnly bool) error {
	for _, def := range defs {
		if userInputOnly && !def.Editable {
			continue
		}
		value := strings.TrimSpace(values[def.Name])
		if def.Required && value == "" {
			return fmt.Errorf("parameter %q is required", def.Name)
		}
		if def.ValidationRE != "" && value != "" {
			pattern, err := regexp.Compile(def.ValidationRE)
			if err != nil {
				return fmt.Errorf("invalid validation regex for parameter %q", def.Name)
			}
			if !pattern.MatchString(value) {
				return fmt.Errorf("parameter %q does not match validation rule", def.Name)
			}
		}
		if len(def.AllowedValues) > 0 && value != "" {
			matched := false
			for _, allowed := range def.AllowedValues {
				if value == allowed {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("parameter %q has unsupported value", def.Name)
			}
		}
	}
	return nil
}

func mergeExecPolicyValues(defs []ExecPolicyParameterDefinition, bindingValues, userValues map[string]string) map[string]string {
	merged := make(map[string]string, len(defs))
	for _, def := range defs {
		if def.DefaultValue != "" {
			merged[def.Name] = def.DefaultValue
		}
	}
	for key, value := range bindingValues {
		merged[key] = value
	}
	for _, def := range defs {
		if !def.Editable {
			continue
		}
		if value, ok := userValues[def.Name]; ok {
			merged[def.Name] = value
		}
	}
	return merged
}

func (h *Handlers) resolveExecPolicyTask(ctx context.Context, principal *auth.Principal, policyID, agentID uuid.UUID, userValues map[string]string) (*ExecPolicyResolvedTask, error) {
	if h.storage.ExecPolicy == nil {
		return nil, fmt.Errorf("exec policy storage is not configured")
	}
	if principal != nil && !principal.HasExecPolicyPermission(policyID, auth.ExecPolicyPermissionUse) {
		return nil, fmt.Errorf("forbidden")
	}

	policy, err := h.storage.ExecPolicy.GetExecCommandPolicyByID(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if !policy.IsActive {
		return nil, fmt.Errorf("policy is inactive")
	}

	binding, err := h.storage.ExecPolicy.GetExecCommandPolicyBinding(ctx, policyID, agentID)
	if err != nil {
		return nil, err
	}
	if !binding.IsActive {
		return nil, fmt.Errorf("binding is inactive")
	}

	defs, err := parseExecPolicyParameterSchema(policy.ParameterSchema)
	if err != nil {
		return nil, err
	}
	bindingValues, err := parseJSONStringMap(binding.ParameterValues)
	if err != nil {
		return nil, err
	}
	if err := validateExecPolicyValues(defs, bindingValues, false); err != nil {
		return nil, err
	}
	if err := validateExecPolicyValues(defs, userValues, true); err != nil {
		return nil, err
	}
	mergedValues := mergeExecPolicyValues(defs, bindingValues, userValues)
	if err := validateExecPolicyValues(defs, mergedValues, true); err != nil {
		return nil, err
	}

	commandTemplate := policy.CommandTemplate
	if binding.CommandTemplateOverride.Valid && strings.TrimSpace(binding.CommandTemplateOverride.String) != "" {
		commandTemplate = binding.CommandTemplateOverride.String
	}
	argsTemplate := []string(policy.ArgsTemplate)
	if len(binding.ArgsTemplateOverride) > 0 {
		argsTemplate = []string(binding.ArgsTemplateOverride)
	}

	resolved := &ExecPolicyResolvedTask{
		PolicyID:  policy.ID,
		BindingID: binding.ID,
		Command:   interpolateExecTemplate(commandTemplate, mergedValues),
		Args:      make([]string, 0, len(argsTemplate)),
	}
	for _, arg := range argsTemplate {
		resolved.Args = append(resolved.Args, interpolateExecTemplate(arg, mergedValues))
	}
	return resolved, nil
}

func (h *Handlers) accessibleExecPolicies(ctx context.Context, includeDetails bool) ([]ExecPolicyView, error) {
	if h.storage.ExecPolicy == nil {
		return nil, nil
	}
	policies, err := h.storage.ExecPolicy.ListExecCommandPolicies(ctx)
	if err != nil {
		return nil, err
	}
	principal := auth.PrincipalFromContext(ctx)
	result := make([]ExecPolicyView, 0, len(policies))
	for _, policy := range policies {
		if policy == nil || !policy.IsActive {
			continue
		}
		if principal != nil && !principal.HasExecPolicyPermission(policy.ID, auth.ExecPolicyPermissionUse) {
			continue
		}
		defs, err := parseExecPolicyParameterSchema(policy.ParameterSchema)
		if err != nil {
			return nil, err
		}
		view := ExecPolicyView{
			ID:          policy.ID.String(),
			Name:        policy.Name,
			Description: policy.Description,
			Parameters:  defs,
		}
		if includeDetails {
			for i := range view.Parameters {
				if view.Parameters[i].Secret {
					view.Parameters[i].DefaultValue = ""
				}
			}
		}
		result = append(result, view)
	}
	return result, nil
}

func (h *Handlers) buildExecPolicyCards(ctx context.Context, policies []*storage.ExecCommandPolicy, bindings []*storage.ExecCommandPolicyBinding, agents []*storage.Agent) ([]*ExecPolicyCard, error) {
	agentNames := make(map[uuid.UUID]string, len(agents))
	for _, agent := range agents {
		if agent != nil {
			agentNames[agent.ID] = agent.Hostname
		}
	}

	bindingsByPolicy := make(map[uuid.UUID][]*storage.ExecCommandPolicyBinding)
	for _, binding := range bindings {
		if binding != nil {
			bindingsByPolicy[binding.PolicyID] = append(bindingsByPolicy[binding.PolicyID], binding)
		}
	}

	cards := make([]*ExecPolicyCard, 0, len(policies))
	for _, policy := range policies {
		if policy == nil {
			continue
		}
		defs, err := parseExecPolicyParameterSchema(policy.ParameterSchema)
		if err != nil {
			return nil, err
		}
		card := &ExecPolicyCard{
			Policy:           policy,
			VisibleCommand:   policy.CommandTemplate,
			VisibleArgs:      []string(policy.ArgsTemplate),
			CanViewDetails:   true,
			ParsedParameters: defs,
			Bindings:         make([]*ExecPolicyBindingCard, 0, len(bindingsByPolicy[policy.ID])),
		}
		for _, binding := range bindingsByPolicy[policy.ID] {
			params, err := parseJSONStringMap(binding.ParameterValues)
			if err != nil {
				return nil, err
			}
			paramJSON, _ := json.MarshalIndent(params, "", "  ")
			visibleCommand := policy.CommandTemplate
			if binding.CommandTemplateOverride.Valid && binding.CommandTemplateOverride.String != "" {
				visibleCommand = binding.CommandTemplateOverride.String
			}
			visibleArgs := []string(policy.ArgsTemplate)
			if len(binding.ArgsTemplateOverride) > 0 {
				visibleArgs = []string(binding.ArgsTemplateOverride)
			}
			card.Bindings = append(card.Bindings, &ExecPolicyBindingCard{
				Binding:          binding,
				AgentName:        agentNames[binding.AgentID],
				VisibleCommand:   visibleCommand,
				VisibleArgs:      visibleArgs,
				VisibleParamJSON: string(paramJSON),
			})
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func normalizeExecTemplateArgs(raw string) pq.StringArray {
	if strings.TrimSpace(raw) == "" {
		return pq.StringArray{}
	}
	parts := strings.Split(raw, ",")
	result := make(pq.StringArray, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		result = append(result, trimmed)
	}
	return result
}

func buildExecPolicyRole(policyID uuid.UUID, permission string) *storage.Role {
	return &storage.Role{
		Name:        auth.RoleForExecPolicy(policyID, permission),
		Description: fmt.Sprintf("Exec policy %s permission: %s", policyID.String(), permission),
	}
}

func mustNullString(value string) sql.NullString {
	trimmed := strings.TrimSpace(value)
	return sql.NullString{String: trimmed, Valid: trimmed != ""}
}

func execPolicyPresets() []ExecPolicyPreset {
	return []ExecPolicyPreset{
		{
			Key:             "ping",
			Name:            "Ping Host",
			Description:     "Ping a host with a per-agent target and optional count.",
			CommandTemplate: "ping",
			ArgsTemplate:    []string{"-n", "{{count}}", "{{target}}"},
			ParameterSchema: []ExecPolicyParameterDefinition{
				{Name: "target", Label: "Target", Type: "text", Required: true, Editable: false},
				{Name: "count", Label: "Count", Type: "number", Required: true, Editable: true, DefaultValue: "4"},
			},
		},
		{
			Key:             "powershell",
			Name:            "PowerShell Script",
			Description:     "Run a PowerShell command with one editable argument.",
			CommandTemplate: "powershell.exe",
			ArgsTemplate:    []string{"-NoProfile", "-Command", "{{script}}"},
			ParameterSchema: []ExecPolicyParameterDefinition{
				{Name: "script", Label: "Script", Type: "text", Required: true, Editable: true},
			},
		},
		{
			Key:             "cmd",
			Name:            "CMD Command",
			Description:     "Run a classic Windows cmd.exe command.",
			CommandTemplate: "cmd.exe",
			ArgsTemplate:    []string{"/c", "{{command_line}}"},
			ParameterSchema: []ExecPolicyParameterDefinition{
				{Name: "command_line", Label: "Command Line", Type: "text", Required: true, Editable: true},
			},
		},
	}
}
