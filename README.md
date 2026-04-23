# gh-standup

A GitHub CLI extension that generates AI-powered standup reports using GitHub activity data. It uses [GitHub Models](https://docs.github.com/en/github-models) for inference by default, and can also use GitHub Copilot as an alternative backend.

## Installation

```bash
gh extension install sgoedecke/gh-standup
gh standup
```

### Organizations

To ensure the GitHub CLI can access your organization's data:

```bash
# Authenticate with GitHub CLI (if not already done)
gh auth login

# Authenticate with your organizations
gh auth refresh -h github.com -s read:org
```

### Prerequisites

- [GitHub CLI](https://cli.github.com/) installed and authenticated
- Optional for `--provider copilot`: [GitHub Copilot CLI](https://docs.github.com/en/copilot/how-tos/set-up/install-copilot-cli) installed and authenticated

## Usage

### Basic Usage

Generate a standup report for yesterday's activity:

```bash
gh standup
```

### Advanced Options

```bash
# Look back multiple days
gh standup --days 3

# Generate report for specific user
gh standup --user octocat

# Generate report for specific repository
gh standup --repo owner/repo

# Use a different AI model
gh standup --model xai/grok-3-mini

# Use GitHub Copilot instead of GitHub Models
gh standup --provider copilot

# Use a specific GitHub Copilot model
gh standup --provider copilot --model gpt-4.1

# Choose your language (default: English)
gh standup --language english
```

## Contributing

Contributions are welcome. In particular, I encourage tweaking of the [prompt](https://github.com/sgoedecke/gh-standup/blob/main/internal/llm/standup.prompt.yml). Since I've extracted it into a file, you should be able to fork the repo and iterate on the prompt via the GitHub Models UI:

`https://github.com/[your-username]/gh-standup/models/prompt/compare/main/internal/llm/standup.prompt.yml`
