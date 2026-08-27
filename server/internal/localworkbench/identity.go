package localworkbench

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

type IdentityContext struct {
	Operator  User      `json:"operator"`
	Workspace Workspace `json:"workspace"`
}

type User struct {
	ID                      string         `json:"id"`
	Name                    string         `json:"name"`
	Email                   string         `json:"email"`
	AvatarURL               *string        `json:"avatar_url"`
	OnboardedAt             *string        `json:"onboarded_at"`
	OnboardingQuestionnaire map[string]any `json:"onboarding_questionnaire"`
	StarterContentState     *string        `json:"starter_content_state"`
	Language                *string        `json:"language"`
	ProfileDescription      string         `json:"profile_description"`
	Timezone                *string        `json:"timezone"`
	CreatedAt               string         `json:"created_at"`
	UpdatedAt               string         `json:"updated_at"`
	questionnaireJSON       string
}

type Workspace struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Slug         string          `json:"slug"`
	Description  *string         `json:"description"`
	Context      *string         `json:"context"`
	Settings     map[string]any  `json:"settings"`
	Repos        []WorkspaceRepo `json:"repos"`
	IssuePrefix  string          `json:"issue_prefix"`
	AvatarURL    *string         `json:"avatar_url"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	settingsJSON string
	reposJSON    string
}

type WorkspaceRepo struct {
	URL         string  `json:"url"`
	Description *string `json:"description,omitempty"`
}

func (identity *IdentityContext) decodeJSON() error {
	if err := json.Unmarshal([]byte(identity.Operator.questionnaireJSON), &identity.Operator.OnboardingQuestionnaire); err != nil {
		return fmt.Errorf("decode local operator questionnaire: %w", err)
	}
	if err := json.Unmarshal([]byte(identity.Workspace.settingsJSON), &identity.Workspace.Settings); err != nil {
		return fmt.Errorf("decode local workspace settings: %w", err)
	}
	if err := json.Unmarshal([]byte(identity.Workspace.reposJSON), &identity.Workspace.Repos); err != nil {
		return fmt.Errorf("decode local workspace repos: %w", err)
	}
	return nil
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	identity, err := s.Identity(ctx)
	if err != nil {
		return nil, err
	}
	return []Workspace{identity.Workspace}, nil
}

func (s *Store) GetWorkspace(ctx context.Context, value string) (Workspace, error) {
	identity, err := s.Identity(ctx)
	if err != nil {
		return Workspace{}, err
	}
	if value != identity.Workspace.ID && value != identity.Workspace.Slug {
		return Workspace{}, sql.ErrNoRows
	}
	return identity.Workspace, nil
}
