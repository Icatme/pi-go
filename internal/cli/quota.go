package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Icatme/pi-go/pkg/pigo"
)

var queryQuotaFn = pigo.QueryQuota

const quotaUsage = "Usage: pigo quota [--json] <provider>\n"

func runQuota(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("quota", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if err := writeQuotaOutput(stdout, quotaUsage); err != nil {
				writeLine(stderr, "Failed to write quota output: "+err.Error()+"\n")
				return 1
			}
			return 0
		}
		writeLine(stderr, quotaUsage)
		return 1
	}
	if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
		writeLine(stderr, quotaUsage)
		return 1
	}

	authPath, err := resolveAuthPath()
	if err != nil {
		writeLine(stderr, err.Error()+"\n")
		return 1
	}
	result, err := queryQuotaFn(ctx, pigo.Provider(strings.TrimSpace(flags.Arg(0))), pigo.QuotaQueryOptions{
		Auth: loadRuntimeAuth(authPath),
	})
	if err != nil {
		writeLine(stderr, "Quota query failed: "+err.Error()+"\n")
		return 1
	}

	output := formatQuota(result)
	if *jsonOutput {
		body, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			writeLine(stderr, "Failed to encode quota output: "+err.Error()+"\n")
			return 1
		}
		output = string(body) + "\n"
	}
	if err := writeQuotaOutput(stdout, output); err != nil {
		writeLine(stderr, "Failed to write quota output: "+err.Error()+"\n")
		return 1
	}
	for _, issue := range result.Unavailable {
		warning := fmt.Sprintf("Warning: %s: %s", issue.Section, issue.Message)
		if issue.HTTPStatus != 0 {
			warning += fmt.Sprintf(" (HTTP %d)", issue.HTTPStatus)
		}
		if err := writeQuotaOutput(stderr, warning+"\n"); err != nil {
			return 1
		}
	}
	return 0
}

func formatQuota(result pigo.QuotaResult) string {
	var output strings.Builder
	fmt.Fprintf(&output, "Provider: %s\nQueried at: %s\n", result.Provider, quotaTime(&result.QueriedAt))
	if subscription := result.Subscription; subscription != nil {
		fmt.Fprintf(&output, "Subscription: %s (%s)\nPeriod: %s - %s\n",
			quotaText(subscription.Plan), quotaText(subscription.Status),
			quotaTime(subscription.PeriodStart), quotaTime(subscription.PeriodEnd))
	} else {
		output.WriteString("Subscription: unknown\n")
	}
	output.WriteString("\nBalances:\n")
	if len(result.Balances) == 0 {
		output.WriteString("  (none reported)\n")
	}
	for _, balance := range result.Balances {
		fmt.Fprintf(&output, "  %s [%s]: remaining=%s, used=%s, total=%s\n",
			balance.ID, quotaText(balance.Unit), quotaAmount(balance.Remaining), quotaAmount(balance.Used), quotaAmount(balance.Total))
	}
	output.WriteString("\nWindows (independent limits):\n")
	if len(result.Windows) == 0 {
		output.WriteString("  (none reported)\n")
	}
	for _, window := range result.Windows {
		fmt.Fprintf(&output, "  %s [%s]: remaining=%s, used=%s, limit=%s, resets=%s",
			window.ID, quotaText(window.Unit), quotaAmount(window.Remaining), quotaAmount(window.Used), quotaAmount(window.Limit), quotaTime(window.ResetsAt))
		if len(window.BalanceIDs) > 0 {
			fmt.Fprintf(&output, ", applies-to=%s", strings.Join(window.BalanceIDs, ","))
		}
		output.WriteByte('\n')
	}
	return output.String()
}

func quotaAmount(value *float64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func quotaTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "unknown"
	}
	return value.Format(time.RFC3339)
}

func quotaText(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func writeQuotaOutput(out io.Writer, output string) error {
	if out == nil {
		return nil
	}
	n, err := io.WriteString(out, output)
	if err == nil && n != len(output) {
		return io.ErrShortWrite
	}
	return err
}
