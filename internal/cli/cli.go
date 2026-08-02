package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Icatme/pi-go/pkg/pigo"
)

var (
	getProvidersFn             = pigo.GetProviders
	getModelsFn                = pigo.GetModels
	getModelFn                 = pigo.GetModel
	getEnvAPIKeyFn             = pigo.GetEnvAPIKey
	requiresOAuthFn            = pigo.RequiresOAuth
	completeSimpleFn           = pigo.CompleteSimple
	refreshCommandCodeModelsFn = pigo.RefreshCommandCodeModels
	resolveCommandCodeAPIKeyFn = pigo.ResolveCommandCodeAPIKey
)

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}

	command := ""
	if len(args) > 0 {
		command = args[0]
	}

	switch command {
	case "", "help", "--help", "-h":
		writeUsage(stdout)
		return 0
	case "models":
		return runModels(ctx, args[1:], stdout, stderr)
	case "list":
		return runList(stdout)
	case "login":
		return runLogin(ctx, args[1:], stdin, stdout, stderr)
	case "ask":
		return runAsk(ctx, args[1:], stdin, stdout, stderr)
	default:
		writeLine(stderr, fmt.Sprintf("Unknown command: %s\n", command))
		writeLine(stderr, "Use 'pigo --help' for usage.\n")
		return 1
	}
}

func writeUsage(stdout io.Writer) {
	providers := getOAuthProviders()
	lines := make([]string, 0, len(providers))
	for _, provider := range providers {
		lines = append(lines, fmt.Sprintf("  %s%s", provider.ID(), strings.Repeat(" ", max(1, 20-len(provider.ID())))+provider.Name()))
	}

	writeLine(stdout, fmt.Sprintf("pigo %s\n\n", pigo.Version))
	writeLine(stdout, "Usage: pigo <command> [provider]\n\n")
	writeLine(stdout, "Commands:\n")
	writeLine(stdout, "  ask               Send a prompt with local .pigo credentials\n")
	writeLine(stdout, "  login [provider]  Login to an OAuth provider\n")
	writeLine(stdout, "  list              List available OAuth providers\n")
	writeLine(stdout, "  models [provider] List available models\n\n")
	writeLine(stdout, "Providers:\n")
	for _, line := range lines {
		writeLine(stdout, line+"\n")
	}
	writeLine(stdout, "\nExamples:\n")
	writeLine(stdout, "  pigo ask --provider openai-codex --model gpt-5.4 \"hello\"\n")
	writeLine(stdout, "  pigo ask --provider kimi-coding \"hello\"\n")
	writeLine(stdout, "  pigo login\n")
	writeLine(stdout, "  pigo login openai-codex\n")
	writeLine(stdout, "  pigo list\n")
	writeLine(stdout, "  pigo models openai-codex\n")
}

func runList(stdout io.Writer) int {
	writeLine(stdout, "Available OAuth providers:\n\n")
	for _, provider := range getOAuthProviders() {
		writeLine(stdout, fmt.Sprintf("  %s%s\n", provider.ID(), strings.Repeat(" ", max(1, 20-len(provider.ID())))+provider.Name()))
	}
	return 0
}

func runModels(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		writeLine(stderr, "Usage: pigo models [provider]\n")
		return 1
	}

	if len(args) == 1 {
		provider := pigo.Provider(strings.TrimSpace(args[0]))
		if err := refreshProviderModels(ctx, provider); err != nil {
			writeLine(stderr, err.Error()+"\n")
			return 1
		}
		models := getModelsFn(provider)
		if len(models) == 0 {
			writeLine(stderr, fmt.Sprintf("Unknown provider or no models: %s\n", provider))
			return 1
		}
		writeLine(stdout, fmt.Sprintf("%s models:\n\n", provider))
		for _, model := range models {
			writeLine(stdout, fmt.Sprintf("  %s\n", model.ID))
		}
		return 0
	}
	if err := refreshProviderModels(ctx, "commandcode"); err != nil {
		writeLine(stderr, "Warning: "+err.Error()+"; using currently registered Command Code models.\n")
	}

	for _, provider := range getProvidersFn() {
		models := getModelsFn(provider)
		if len(models) == 0 {
			continue
		}
		writeLine(stdout, fmt.Sprintf("%s:\n", provider))
		for _, model := range models {
			writeLine(stdout, fmt.Sprintf("  %s\n", model.ID))
		}
		writeLine(stdout, "\n")
	}
	return 0
}

func runLogin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	providers := getOAuthProviders()
	if len(providers) == 0 {
		writeLine(stderr, "No OAuth providers are available.\n")
		return 1
	}

	providerID := ""
	if len(args) > 0 {
		providerID = strings.TrimSpace(args[0])
	} else if len(providers) == 1 {
		providerID = providers[0].ID()
	} else {
		selected, err := promptProviderSelection(stdin, stdout, providers)
		if err != nil {
			writeLine(stderr, err.Error()+"\n")
			return 1
		}
		providerID = selected
	}

	provider := getOAuthProvider(providerID)
	if provider == nil {
		writeLine(stderr, fmt.Sprintf("Unknown provider: %s\n", providerID))
		writeLine(stderr, "Use 'pigo list' to see available OAuth providers.\n")
		return 1
	}

	authPath, err := resolveAuthPath()
	if err != nil {
		writeLine(stderr, err.Error()+"\n")
		return 1
	}

	reader := bufio.NewReader(stdin)
	credentials, err := provider.Login(ctx, oauthLoginCallbacks{
		OnAuth: func(info oauthAuthInfo) {
			writeLine(stdout, "\nOpen this URL in your browser:\n")
			writeLine(stdout, info.URL+"\n")
			if strings.TrimSpace(info.Instructions) != "" {
				writeLine(stdout, info.Instructions+"\n")
			}
			writeLine(stdout, "\n")
		},
		OnPrompt: func(prompt oauthPrompt) (string, error) {
			writeLine(stdout, prompt.Message+" ")
			line, readErr := reader.ReadString('\n')
			if readErr != nil && readErr != io.EOF {
				return "", readErr
			}
			return strings.TrimSpace(line), nil
		},
		OnOutput: func(line string) {
			writeLine(stdout, line)
		},
	})
	if err != nil {
		writeLine(stderr, "Login failed: "+err.Error()+"\n")
		return 1
	}

	auth := loadAuth(authPath)
	auth[provider.ID()] = credentials
	if err := saveAuth(authPath, auth); err != nil {
		writeLine(stderr, "Failed to save auth.json: "+err.Error()+"\n")
		return 1
	}

	writeLine(stdout, "\nCredentials saved to .pigo/auth.json\n")
	return 0
}

func runAsk(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ask", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	providerID := flags.String("provider", "openai-codex", "")
	modelID := flags.String("model", "", "")
	systemPrompt := flags.String("system", "", "")
	reasoning := flags.String("reasoning", "", "")
	maxTokens := flags.Int("max-tokens", 0, "")

	if err := flags.Parse(args); err != nil {
		writeLine(stderr, "Usage: pigo ask --provider <provider> [--model <model>] [--system <prompt>] [--reasoning <level>] [--max-tokens <n>] <prompt>\n")
		return 1
	}

	prompt := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if prompt == "" {
		body, err := io.ReadAll(stdin)
		if err != nil {
			writeLine(stderr, "Failed to read prompt: "+err.Error()+"\n")
			return 1
		}
		prompt = strings.TrimSpace(string(body))
	}
	if prompt == "" {
		writeLine(stderr, "Missing prompt.\n")
		return 1
	}

	provider := pigo.Provider(strings.TrimSpace(*providerID))
	if err := refreshProviderModels(ctx, provider); err != nil {
		writeLine(stderr, err.Error()+"\n")
		return 1
	}
	resolvedModelID := strings.TrimSpace(*modelID)
	if resolvedModelID == "" {
		resolvedModelID = defaultModelID(provider)
	}
	if resolvedModelID == "" {
		writeLine(stderr, fmt.Sprintf("No default model for provider: %s\n", provider))
		return 1
	}

	model := getModelFn(provider, resolvedModelID)
	if model == nil {
		writeLine(stderr, fmt.Sprintf("Unknown model %s for provider %s\n", resolvedModelID, provider))
		return 1
	}

	authPath, err := resolveAuthPath()
	if err != nil {
		writeLine(stderr, err.Error()+"\n")
		return 1
	}
	auth := loadRuntimeAuth(authPath)

	apiKey := getEnvAPIKeyFn(provider)
	if provider == "commandcode" {
		apiKey = resolveCommandCodeAPIKeyFn(auth)
	}
	options := pigo.SimpleStreamOptions{
		Auth:           auth,
		APIKey:         apiKey,
		RequestContext: ctx,
		MaxTokens:      *maxTokens,
	}
	if normalized := normalizeThinkingLevel(*reasoning); normalized != "" {
		options.Reasoning = normalized
	}

	if options.APIKey == "" && auth[provider].OAuth == nil {
		if requiresOAuthFn(provider) {
			writeLine(stderr, fmt.Sprintf("Missing OAuth credentials for %s. Run 'pigo login %s'.\n", provider, provider))
		} else if getOAuthProvider(string(provider)) != nil {
			writeLine(stderr, fmt.Sprintf("Missing credentials for %s. Run 'pigo login %s' or configure an API key.\n", provider, provider))
		} else {
			writeLine(stderr, fmt.Sprintf("Missing API key for %s. Put it in .pigo/.env.\n", provider))
		}
		return 1
	}

	result := completeSimpleFn(*model, pigo.Context{
		SystemPrompt: defaultAskSystemPrompt(provider, resolvedModelID, strings.TrimSpace(*systemPrompt)),
		Messages: []pigo.Message{
			pigo.UserMessage{Content: prompt},
		},
	}, options)
	if result.StopReason == pigo.StopReasonError {
		writeLine(stderr, result.ErrorMessage+"\n")
		return 1
	}

	output := renderAssistantText(result)
	if strings.TrimSpace(output) != "" {
		writeLine(stdout, output)
		if !strings.HasSuffix(output, "\n") {
			writeLine(stdout, "\n")
		}
	}
	return 0
}

func refreshProviderModels(ctx context.Context, provider pigo.Provider) error {
	if provider != "commandcode" {
		return nil
	}
	_, err := refreshCommandCodeModelsFn(ctx, nil)
	if err != nil {
		return fmt.Errorf("refresh Command Code models: %w", err)
	}
	return nil
}

func promptProviderSelection(stdin io.Reader, stdout io.Writer, providers []oauthProvider) (string, error) {
	reader := bufio.NewReader(stdin)

	writeLine(stdout, "Select a provider:\n\n")
	for index, provider := range providers {
		writeLine(stdout, fmt.Sprintf("  %d. %s\n", index+1, provider.Name()))
	}
	writeLine(stdout, "\nEnter number: ")

	choice, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return "", fmt.Errorf("invalid selection")
	}

	for index := range providers {
		if choice == fmt.Sprintf("%d", index+1) {
			return providers[index].ID(), nil
		}
	}
	return "", fmt.Errorf("invalid selection")
}

func defaultModelID(provider pigo.Provider) string {
	switch provider {
	case "openai-codex":
		return "gpt-5.4"
	case "kimi-coding":
		return "kimi-k2-thinking"
	case "anthropic":
		return "claude-sonnet-4-5"
	}

	models := getModelsFn(provider)
	if len(models) == 0 {
		return ""
	}
	return models[0].ID
}

func normalizeThinkingLevel(level string) pigo.ThinkingLevel {
	value := strings.TrimSpace(strings.ToLower(level))
	switch value {
	case string(pigo.ThinkingLevelMinimal):
		return pigo.ThinkingLevelMinimal
	case string(pigo.ThinkingLevelLow):
		return pigo.ThinkingLevelLow
	case string(pigo.ThinkingLevelMedium):
		return pigo.ThinkingLevelMedium
	case string(pigo.ThinkingLevelHigh):
		return pigo.ThinkingLevelHigh
	case string(pigo.ThinkingLevelXHigh):
		return pigo.ThinkingLevelXHigh
	default:
		return ""
	}
}

func defaultAskSystemPrompt(provider pigo.Provider, modelID string, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}

	if provider == "openai-codex" && modelID == "gpt-5.4" {
		return "You are a helpful assistant. Answer directly and concisely."
	}

	return ""
}

func renderAssistantText(message pigo.AssistantMessage) string {
	parts := make([]string, 0, len(message.Content))
	for _, block := range message.Content {
		switch typed := block.(type) {
		case pigo.TextContent:
			if strings.TrimSpace(typed.Text) != "" {
				parts = append(parts, typed.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
