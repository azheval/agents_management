package auth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"agent-management/server/internal/storage"

	"github.com/google/uuid"
)

const (
	RoleAdmin        = "admin"
	RoleFullAccess   = "full_access"
	ActionRolePrefix = "action."
	AgentRolePrefix  = "agent:"
	PolicyRolePrefix = "exec_policy:"
	basicAuthRealm   = `Basic realm="agent-management"`
)

const (
	AgentPermissionView             = "view"
	AgentPermissionMetricsView      = "metrics_view"
	AgentPermissionTaskView         = "task_view"
	AgentPermissionTaskCreate       = "task_create"
	AgentPermissionTaskReschedule   = "task_reschedule"
	AgentPermissionNotificationView = "notification_view"
	AgentPermissionStatusToggle     = "status_toggle"
	AgentPermissionDelete           = "delete"
)

const (
	ExecPolicyPermissionView   = "view"
	ExecPolicyPermissionUse    = "use"
	ExecPolicyPermissionManage = "manage"
)

type contextKey string

const principalContextKey contextKey = "principal"

type Principal struct {
	Username string
	Roles    map[string]struct{}
}

func NewPrincipal(username string, roles []string) *Principal {
	normalizedRoles := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		trimmed := strings.TrimSpace(strings.ToLower(role))
		if trimmed == "" {
			continue
		}
		normalizedRoles[trimmed] = struct{}{}
	}

	return &Principal{
		Username: username,
		Roles:    normalizedRoles,
	}
}

func (p *Principal) IsAdmin() bool {
	if p == nil {
		return false
	}
	if _, ok := p.Roles[RoleAdmin]; ok {
		return true
	}
	_, ok := p.Roles[RoleFullAccess]
	return ok
}

func (p *Principal) HasRole(role string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Roles[strings.ToLower(strings.TrimSpace(role))]
	return ok
}

func (p *Principal) CanAccessAgent(agentID uuid.UUID) bool {
	return p == nil || p.IsAdmin() || p.HasAnyAgentPermission(agentID)
}

func (p *Principal) CanRunTaskType(taskType storage.TaskType) bool {
	return p == nil || p.IsAdmin() || p.HasRole(RoleForTaskType(taskType))
}

func (p *Principal) AccessibleAgentIDs(agents []*storage.Agent) map[uuid.UUID]struct{} {
	allowed := make(map[uuid.UUID]struct{}, len(agents))
	for _, agent := range agents {
		if agent != nil && p.CanAccessAgent(agent.ID) {
			allowed[agent.ID] = struct{}{}
		}
	}
	return allowed
}

func RoleForTaskType(taskType storage.TaskType) string {
	switch taskType {
	case storage.TaskTypeExecCommand:
		return ActionRolePrefix + "exec_command"
	case storage.TaskTypeExecPythonScript:
		return ActionRolePrefix + "exec_python_script"
	case storage.TaskTypeFetchFile:
		return ActionRolePrefix + "fetch_file"
	case storage.TaskTypePushFile:
		return ActionRolePrefix + "push_file"
	case storage.TaskTypeAgentUpdate:
		return ActionRolePrefix + "agent_update"
	default:
		return ActionRolePrefix + strings.ToLower(string(taskType))
	}
}

func RoleForAgent(agentID uuid.UUID) string {
	return AgentRolePrefix + strings.ToLower(agentID.String())
}

func RoleForAgentPermission(agentID uuid.UUID, permission string) string {
	return RoleForAgent(agentID) + ":" + strings.ToLower(strings.TrimSpace(permission))
}

func (p *Principal) HasAnyAgentPermission(agentID uuid.UUID) bool {
	if p == nil {
		return false
	}
	if p.IsAdmin() || p.HasRole(RoleForAgent(agentID)) {
		return true
	}

	prefix := RoleForAgent(agentID) + ":"
	for role := range p.Roles {
		if strings.HasPrefix(role, prefix) {
			return true
		}
	}
	return false
}

func (p *Principal) HasAgentPermission(agentID uuid.UUID, permission string) bool {
	if p == nil {
		return false
	}
	return p.IsAdmin() ||
		p.HasRole(RoleForAgent(agentID)) ||
		p.HasRole(RoleForAgentPermission(agentID, permission))
}

func RoleForExecPolicy(policyID uuid.UUID, permission string) string {
	return PolicyRolePrefix + strings.ToLower(policyID.String()) + ":" + strings.ToLower(strings.TrimSpace(permission))
}

func (p *Principal) HasExecPolicyPermission(policyID uuid.UUID, permission string) bool {
	if p == nil {
		return false
	}
	return p.IsAdmin() || p.HasRole(RoleForExecPolicy(policyID, permission))
}

func WithPrincipal(ctx context.Context, principal *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func PrincipalFromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(principalContextKey).(*Principal)
	return principal
}

type Authenticator struct {
	users storage.UserRepository
}

func NewAuthenticator(users storage.UserRepository) *Authenticator {
	return &Authenticator{users: users}
}

func (a *Authenticator) Enabled() bool {
	return a != nil && a.users != nil
}

func (a *Authenticator) AuthenticateRequest(r *http.Request) (*Principal, error) {
	if !a.Enabled() {
		return nil, nil
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return nil, errors.New("missing basic auth credentials")
	}

	return a.Authenticate(username, password)
}

func (a *Authenticator) AuthenticateAuthorizationHeader(headerValue string) (*Principal, error) {
	if !a.Enabled() {
		return nil, nil
	}
	if headerValue == "" {
		return nil, errors.New("missing authorization header")
	}
	if !strings.HasPrefix(strings.ToLower(headerValue), "basic ") {
		return nil, errors.New("unsupported authorization scheme")
	}

	payload := strings.TrimSpace(headerValue[len("Basic "):])
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("decode basic auth: %w", err)
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return nil, errors.New("invalid basic auth payload")
	}

	return a.Authenticate(parts[0], parts[1])
}

func (a *Authenticator) Authenticate(username, password string) (*Principal, error) {
	if !a.Enabled() {
		return nil, nil
	}

	user, err := a.users.AuthenticateUser(context.Background(), username, password)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("invalid credentials")
		}
		return nil, err
	}
	if user == nil {
		return nil, errors.New("invalid credentials")
	}

	return NewPrincipal(user.Username, user.Roles), nil
}

func Challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", basicAuthRealm)
	http.Error(w, "Unauthorized", http.StatusUnauthorized)
}

func LogoutChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", basicAuthRealm)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	http.Error(w, "Logged out. Open the page again and sign in with another account.", http.StatusUnauthorized)
}
