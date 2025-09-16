package llm

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/gh-standup/internal/types"
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

type Client struct {
	token string
}

// Simple mapping from model name (lowercase) to a safe default temperature
// to use when the prompt configuration leaves temperature at 0.
var modelTemperatureMap = map[string]float64{
	"openai/gpt-5-mini": 1.0,
	"openai/gpt-5": 1.0,
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

func NewClient() (*Client, error) {
	fmt.Print("  Checking GitHub token... ")

	host, _ := auth.DefaultHost()
	token, _ := auth.TokenForHost(host) // check GH_TOKEN, GITHUB_TOKEN, keychain, etc

	if token == "" {
		fmt.Println("Failed")
		return nil, fmt.Errorf("No GitHub token found. Please run 'gh auth login' to authenticate.")
	}
	fmt.Println("Done")

	return &Client{token: token}, nil
}

func loadPromptConfig() (*PromptConfig, error) {
	var config PromptConfig
	err := yaml.Unmarshal(standupPromptYAML, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse prompt configuration: %w", err)
	}
	return &config, nil
}

func (c *Client) GenerateStandupReport(activities []types.GitHubActivity, model string) (string, error) {
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

	// Use the model from parameter or fall back to config
	selectedModel := model
	if selectedModel == "" {
		selectedModel = promptConfig.Model
	}

	// Build messages from the prompt config, replacing template variables
	messages := make([]Message, len(promptConfig.Messages))
	for i, msg := range promptConfig.Messages {
		content := msg.Content
		// Replace the {{activities}} template variable
		content = strings.ReplaceAll(content, "{{activities}}", activitySummary)

		messages[i] = Message{
			Role:    msg.Role,
			Content: content,
		}
	}

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
