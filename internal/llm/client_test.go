package llm

import (
	"strings"
	"testing"
	"time"

	"github.com/gh-standup/internal/types"
)

func TestFormatActivitiesForLLM(t *testing.T) {
	client := &Client{}

	activities := []types.GitHubActivity{
		{
			Type:        "commit",
			Repository:  "test/repo",
			Title:       "Fix bug in user authentication",
			Description: "Fix bug in user authentication\n\nThis commit resolves the issue where users couldn't log in",
			URL:         "https://github.com/test/repo/commit/abc123",
			CreatedAt:   time.Now(),
		},
		{
			Type:        "pull_request",
			Repository:  "test/repo",
			Title:       "PR #123: Add new feature",
			Description: "This PR adds a new feature for better user experience",
			URL:         "https://github.com/test/repo/pull/123",
			CreatedAt:   time.Now(),
		},
	}

	result := client.formatActivitiesForLLM(activities)

	if result == "" {
		t.Error("Expected non-empty result")
	}

	if !strings.Contains(result, "COMMITS:") {
		t.Error("Expected result to contain COMMITS section")
	}

	if !strings.Contains(result, "PULL REQUESTS:") {
		t.Error("Expected result to contain PULL REQUESTS section")
	}

	if !strings.Contains(result, "Fix bug in user authentication") {
		t.Error("Expected result to contain commit title")
	}

	if !strings.Contains(result, "PR #123: Add new feature") {
		t.Error("Expected result to contain PR title")
	}
}

func TestFormatActivitiesForLLMEmpty(t *testing.T) {
	client := &Client{}
	activities := []types.GitHubActivity{}
	result := client.formatActivitiesForLLM(activities)

	expected := "No GitHub activity found for the specified period."
	if result != expected {
		t.Errorf("Expected %q, got %q", expected, result)
	}
}

func TestParseProvider(t *testing.T) {
	t.Run("default provider", func(t *testing.T) {
		provider, err := ParseProvider("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if provider != ProviderGitHubModels {
			t.Fatalf("expected %q, got %q", ProviderGitHubModels, provider)
		}
	})

	t.Run("copilot provider", func(t *testing.T) {
		provider, err := ParseProvider("copilot")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if provider != ProviderCopilot {
			t.Fatalf("expected %q, got %q", ProviderCopilot, provider)
		}
	})

	t.Run("invalid provider", func(t *testing.T) {
		if _, err := ParseProvider("invalid"); err == nil {
			t.Fatal("expected an error for an invalid provider")
		}
	})
}

func TestResolveModel(t *testing.T) {
	t.Run("github models default", func(t *testing.T) {
		client := &Client{provider: ProviderGitHubModels}
		model := client.resolveModel("", "openai/gpt-4o")
		if model != "openai/gpt-4o" {
			t.Fatalf("expected prompt default model, got %q", model)
		}
	})

	t.Run("copilot default", func(t *testing.T) {
		client := &Client{provider: ProviderCopilot}
		model := client.resolveModel("", "openai/gpt-4o")
		if model != defaultCopilotModel {
			t.Fatalf("expected %q, got %q", defaultCopilotModel, model)
		}
	})

	t.Run("explicit model wins", func(t *testing.T) {
		client := &Client{provider: ProviderCopilot}
		model := client.resolveModel("claude-sonnet-4.5", "openai/gpt-4o")
		if model != "claude-sonnet-4.5" {
			t.Fatalf("expected explicit model, got %q", model)
		}
	})
}

func TestBuildCopilotPrompt(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "Follow the standup format."},
		{Role: "user", Content: "Summarize this activity."},
		{Role: "assistant", Content: "Previous answer context."},
	}

	systemMessage, prompt, err := buildCopilotPrompt(messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if systemMessage != "Follow the standup format." {
		t.Fatalf("unexpected system message: %q", systemMessage)
	}

	if !strings.Contains(prompt, "Summarize this activity.") {
		t.Fatalf("expected user prompt content, got %q", prompt)
	}

	if !strings.Contains(prompt, "ASSISTANT:\nPrevious answer context.") {
		t.Fatalf("expected non-user context to be preserved, got %q", prompt)
	}
}
