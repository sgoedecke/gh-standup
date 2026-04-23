package llm

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/gh-standup/internal/types"
	copilot "github.com/github/copilot-sdk/go"
	"gopkg.in/yaml.v3"
)

//go:embed standup.prompt.yml
var standupPromptYAML []byte

type PromptConfig struct {
	Name            string          `yaml:"name"`
	Description     string          `yaml:"description"`
	Model           string          `yaml:"model"`
	ModelParameters ModelParameters `yaml:"modelParameters"`
	Messages        []PromptMessage `yaml:"messages"`
}

type ModelParameters struct {
	Temperature float64 `yaml:"temperature"`
	TopP        float64 `yaml:"topP"`
}

type PromptMessage struct {
	Role    string `yaml:"role"`
	Content string `yaml:"content"`
}

type Request struct {
	Messages    []Message `json:"messages"`
	Model       string    `json:"model"`
	Temperature float64   `json:"temperature"`
	TopP        float64   `json:"top_p"`
	Stream      bool      `json:"stream"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Response struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type Provider string

const (
	ProviderGitHubModels Provider = "github-models"
	ProviderCopilot      Provider = "copilot"
)

const (
	defaultGitHubModelsModel = "openai/gpt-4o"
	defaultCopilotModel      = "gpt-4.1"
)

type Options struct {
	Provider string
}

type Client struct {
	provider       Provider
	token          string
	copilotCLIPath string
}

// Simple mapping from model name (lowercase) to a safe default temperature
// to use when the prompt configuration leaves temperature at 0.
var modelTemperatureMap = map[string]float64{
	"openai/gpt-5-mini": 1.0,
	"openai/gpt-5":      1.0,
	// Add other models here as needed

}

// getMappedTemperature returns a mapped temperature for the model (if any).
// Matching is case-insensitive.
func getMappedTemperature(model string) (float64, bool) {
	if model == "" {
		return 0, false
	}
	v, ok := modelTemperatureMap[strings.ToLower(model)]
	return v, ok
}

func ParseProvider(value string) (Provider, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ProviderGitHubModels, nil
	}

	switch Provider(normalized) {
	case ProviderGitHubModels, ProviderCopilot:
		return Provider(normalized), nil
	default:
		return "", fmt.Errorf("unsupported inference provider %q (expected %q or %q)", value, ProviderGitHubModels, ProviderCopilot)
	}
}

func NewClient(options Options) (*Client, error) {
	provider, err := ParseProvider(options.Provider)
	if err != nil {
		return nil, err
	}

	switch provider {
	case ProviderGitHubModels:
		fmt.Print("  Checking GitHub token... ")

		host, _ := auth.DefaultHost()
		token, _ := auth.TokenForHost(host) // check GH_TOKEN, GITHUB_TOKEN, keychain, etc

		if token == "" {
			fmt.Println("Failed")
			return nil, fmt.Errorf("no GitHub token found. Please run 'gh auth login' to authenticate")
		}
		fmt.Println("Done")

		return &Client{
			provider: provider,
			token:    token,
		}, nil
	case ProviderCopilot:
		fmt.Print("  Checking GitHub Copilot CLI... ")

		cliPath, err := exec.LookPath("copilot")
		if err != nil {
			fmt.Println("Failed")
			return nil, fmt.Errorf("GitHub Copilot CLI not found. Install it and authenticate before using --provider=%s", ProviderCopilot)
		}
		fmt.Println("Done")

		return &Client{
			provider:       provider,
			copilotCLIPath: cliPath,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported inference provider %q", provider)
	}
}

func loadPromptConfig() (*PromptConfig, error) {
	var config PromptConfig
	err := yaml.Unmarshal(standupPromptYAML, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt configuration: %w", err)
	}
	return &config, nil
}

func (c *Client) GenerateStandupReport(activities []types.GitHubActivity, model string, language string) (string, error) {
	fmt.Print("  Formatting activity data for AI... ")
	activitySummary := c.formatActivitiesForLLM(activities)
	fmt.Println("Done")

	fmt.Print("  Loading prompt configuration... ")
	promptConfig, err := loadPromptConfig()
	if err != nil {
		fmt.Println("Failed")
		return "", err
	}
	fmt.Println("Done")

	selectedModel := c.resolveModel(model, promptConfig.Model)

	messages := buildMessages(promptConfig.Messages, activitySummary, language)

	switch c.provider {
	case ProviderGitHubModels:
		// Temperature precedence:
		// 1. If the model map contains a value for the selected model, use it.
		// 2. Otherwise use the prompt-configured temperature.
		effectiveTemperature := promptConfig.ModelParameters.Temperature
		if mapped, ok := getMappedTemperature(selectedModel); ok {
			effectiveTemperature = mapped
		}

		request := Request{
			Messages:    messages,
			Model:       selectedModel,
			Temperature: effectiveTemperature,
			TopP:        promptConfig.ModelParameters.TopP,
			Stream:      false,
		}

		fmt.Printf("  Calling GitHub Models API (%s)... ", selectedModel)
		response, err := c.callGitHubModels(request)
		if err != nil {
			fmt.Println("Failed")
			return "", err
		}
		fmt.Println("Done")

		if len(response.Choices) == 0 {
			return "", fmt.Errorf("no response generated from the model")
		}

		return strings.TrimSpace(response.Choices[0].Message.Content), nil
	case ProviderCopilot:
		fmt.Printf("  Calling GitHub Copilot (%s)... ", selectedModel)
		response, err := c.callGitHubCopilot(messages, selectedModel)
		if err != nil {
			fmt.Println("Failed")
			return "", err
		}
		fmt.Println("Done")
		return strings.TrimSpace(response), nil
	default:
		return "", fmt.Errorf("unsupported inference provider %q", c.provider)
	}
}

func buildMessages(promptMessages []PromptMessage, activitySummary string, language string) []Message {
	messages := make([]Message, len(promptMessages))
	for i, msg := range promptMessages {
		content := msg.Content
		content = strings.ReplaceAll(content, "{{activities}}", activitySummary)
		content = strings.ReplaceAll(content, "{{language}}", language)

		messages[i] = Message{
			Role:    msg.Role,
			Content: content,
		}
	}

	return messages
}

func (c *Client) resolveModel(requestedModel string, promptDefault string) string {
	if requestedModel != "" {
		return requestedModel
	}

	if c.provider == ProviderCopilot {
		return defaultCopilotModel
	}

	if promptDefault != "" {
		return promptDefault
	}

	return defaultGitHubModelsModel
}

func (c *Client) formatActivitiesForLLM(activities []types.GitHubActivity) string {
	if len(activities) == 0 {
		return "No GitHub activity found for the specified period."
	}

	var builder strings.Builder

	commits := make([]types.GitHubActivity, 0)
	prs := make([]types.GitHubActivity, 0)
	issues := make([]types.GitHubActivity, 0)
	reviews := make([]types.GitHubActivity, 0)

	for _, activity := range activities {
		switch activity.Type {
		case "commit":
			commits = append(commits, activity)
		case "pull_request":
			prs = append(prs, activity)
		case "issue":
			issues = append(issues, activity)
		case "review":
			reviews = append(reviews, activity)
		}
	}

	// Format commits
	if len(commits) > 0 {
		builder.WriteString("COMMITS:\n")
		for _, commit := range commits {
			builder.WriteString(fmt.Sprintf("- [%s] %s\n", commit.Repository, commit.Title))
			if commit.Description != commit.Title {
				// Add first few lines of commit message if different from title
				lines := strings.Split(commit.Description, "\n")
				if len(lines) > 1 && lines[1] != "" {
					builder.WriteString(fmt.Sprintf("  Description: %s\n", strings.TrimSpace(lines[1])))
				}
			}
		}
		builder.WriteString("\n")
	}

	// Format pull requests
	if len(prs) > 0 {
		builder.WriteString("PULL REQUESTS:\n")
		for _, pr := range prs {
			builder.WriteString(fmt.Sprintf("- [%s] %s\n", pr.Repository, pr.Title))
			if pr.Description != "" && len(pr.Description) < 200 {
				builder.WriteString(fmt.Sprintf("  Description: %s\n", strings.TrimSpace(pr.Description)))
			}
		}
		builder.WriteString("\n")
	}

	// Format issues
	if len(issues) > 0 {
		builder.WriteString("ISSUES:\n")
		for _, issue := range issues {
			builder.WriteString(fmt.Sprintf("- [%s] %s\n", issue.Repository, issue.Title))
			if issue.Description != "" && len(issue.Description) < 200 {
				builder.WriteString(fmt.Sprintf("  Description: %s\n", strings.TrimSpace(issue.Description)))
			}
		}
		builder.WriteString("\n")
	}

	// Format reviews
	if len(reviews) > 0 {
		builder.WriteString("CODE REVIEWS:\n")
		for _, review := range reviews {
			builder.WriteString(fmt.Sprintf("- [%s] %s\n", review.Repository, review.Title))
		}
		builder.WriteString("\n")
	}

	return builder.String()
}

// callGitHubModels makes the API call to GitHub Models
func (c *Client) callGitHubModels(request Request) (*Response, error) {
	jsonData, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://models.github.ai/inference/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &response, nil
}

func (c *Client) callGitHubCopilot(messages []Message, selectedModel string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client := copilot.NewClient(&copilot.ClientOptions{
		CLIPath:  c.copilotCLIPath,
		LogLevel: "error",
	})

	if err := client.Start(ctx); err != nil {
		return "", fmt.Errorf("failed to start GitHub Copilot CLI: %w", err)
	}
	defer client.Stop()

	if err := validateCopilotModel(ctx, client, selectedModel); err != nil {
		return "", err
	}

	systemMessage, prompt, err := buildCopilotPrompt(messages)
	if err != nil {
		return "", err
	}

	sessionConfig := &copilot.SessionConfig{
		ClientName:          "gh-standup",
		Model:               selectedModel,
		OnPermissionRequest: denyAllCopilotPermissions,
	}
	if systemMessage != "" {
		sessionConfig.SystemMessage = &copilot.SystemMessageConfig{
			Content: systemMessage,
		}
	}

	session, err := client.CreateSession(ctx, sessionConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create GitHub Copilot session: %w", err)
	}
	defer session.Disconnect()

	response, err := session.SendAndWait(ctx, copilot.MessageOptions{
		Prompt: prompt,
	})
	if err != nil {
		return "", fmt.Errorf("failed to send GitHub Copilot prompt: %w", err)
	}

	if response == nil {
		return "", fmt.Errorf("no response generated from GitHub Copilot")
	}

	assistantMessage, ok := response.Data.(*copilot.AssistantMessageData)
	if !ok || strings.TrimSpace(assistantMessage.Content) == "" {
		return "", fmt.Errorf("no response generated from GitHub Copilot")
	}

	return assistantMessage.Content, nil
}

func validateCopilotModel(ctx context.Context, client *copilot.Client, selectedModel string) error {
	models, err := client.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("failed to list GitHub Copilot models: %w", err)
	}

	availableModelIDs := make([]string, 0, len(models))
	for _, model := range models {
		availableModelIDs = append(availableModelIDs, model.ID)
		if model.ID == selectedModel {
			return nil
		}
	}

	if len(availableModelIDs) == 0 {
		return fmt.Errorf("GitHub Copilot model %q is not available", selectedModel)
	}

	if len(availableModelIDs) > 5 {
		availableModelIDs = availableModelIDs[:5]
	}

	return fmt.Errorf("GitHub Copilot model %q is not available. Available models include: %s", selectedModel, strings.Join(availableModelIDs, ", "))
}

func buildCopilotPrompt(messages []Message) (string, string, error) {
	var systemParts []string
	var promptParts []string

	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}

		switch strings.ToLower(msg.Role) {
		case "system":
			systemParts = append(systemParts, content)
		case "", "user":
			promptParts = append(promptParts, content)
		default:
			promptParts = append(promptParts, fmt.Sprintf("%s:\n%s", strings.ToUpper(msg.Role), content))
		}
	}

	if len(promptParts) == 0 {
		return "", "", fmt.Errorf("no prompt configured for GitHub Copilot inference")
	}

	return strings.Join(systemParts, "\n\n"), strings.Join(promptParts, "\n\n"), nil
}

func denyAllCopilotPermissions(_ copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilot.PermissionRequestResult, error) {
	return copilot.PermissionRequestResult{
		Kind: copilot.PermissionRequestResultKindDeniedByRules,
	}, nil
}
