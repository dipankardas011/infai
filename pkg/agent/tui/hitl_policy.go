package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/dipankardas011/infai/pkg/agent/auditor"
	"github.com/dipankardas011/infai/pkg/agent/contracts"
	"github.com/dipankardas011/infai/pkg/agent/glue"
)

type hitlPolicyManagementClient interface {
	ListHITLPolicies(context.Context) (glue.HITLPoliciesOutput, error)
	CreateHITLPolicy(context.Context, string, contracts.HITLPolicyConfig) error
	RemoveHITLPolicy(context.Context, string) error
}

func hitlPolicyReadOnly(name string) error {
	return fmt.Errorf("HITL policy %q is built in and cannot be changed", name)
}

const hitlPolicySessionNote = "Existing sessions keep their current HITL policy until switched; new and resumed sessions use the latest definition."

var (
	hitlAllowStyle = lipgloss.NewStyle().Foreground(everforest.Green)
	hitlDenyStyle  = lipgloss.NewStyle().Foreground(everforest.Red)
	hitlHumanStyle = lipgloss.NewStyle().Foreground(everforest.Blue)
)

// hitlDecisionStyle is the colour of a decision wherever it is shown: green for
// allow, red for deny, blue for human.
func hitlDecisionStyle(decision auditor.PolicyDecision) lipgloss.Style {
	switch decision {
	case auditor.AllowPolicy:
		return hitlAllowStyle
	case auditor.DenyPolicy:
		return hitlDenyStyle
	default:
		return hitlHumanStyle
	}
}

// hitlDecisionMark is the one-cell mark a decision prints as.
func hitlDecisionMark(decision auditor.PolicyDecision) string {
	switch decision {
	case auditor.AllowPolicy:
		return "✓"
	case auditor.DenyPolicy:
		return "✗"
	default:
		return "i"
	}
}

// hitlPolicyToolRows lists every built-in tool plus any tool the policy names
// itself, so a policy created before a tool existed still lists that tool as
// human rather than leaving it out of the table.
func hitlPolicyToolRows(decisions map[string]string) []string {
	set := make(map[string]struct{}, len(hitlPolicyTools)+len(decisions))
	for _, tool := range hitlPolicyTools {
		set[string(tool)] = struct{}{}
	}
	for tool := range decisions {
		set[tool] = struct{}{}
	}
	return slices.Sorted(maps.Keys(set))
}

func RunHITLPolicyList(ctx context.Context, client hitlPolicyManagementClient, out io.Writer) error {
	output, err := client.ListHITLPolicies(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(output.Policies, func(a, b glue.HITLPolicyView) int {
		return strings.Compare(a.Name, b.Name)
	})
	styles := newHarnessStyles()
	fmt.Fprintln(out, styles.heading("HITL policies", everforest.Background))
	for index, policy := range output.Policies {
		if index > 0 {
			fmt.Fprintln(out)
		}
		label := policy.Name
		if policy.Name == auditor.DefaultHITLPolicyName {
			label += " (built in, read-only)"
		}
		fmt.Fprintln(out, styles.active.Render(label))

		tools := hitlPolicyToolRows(policy.Decisions)
		width := len("Tool")
		for _, tool := range tools {
			width = max(width, len(tool))
		}
		fmt.Fprintln(out, styles.muted.Render(fmt.Sprintf("%-*s  %s", width, "Tool", "Policy")))
		for _, tool := range tools {
			decision := auditor.PolicyDecision(policy.Decisions[tool])
			if decision == "" {
				decision = auditor.HumanPolicy
			}
			fmt.Fprintln(out, styles.screenRow.Render(fmt.Sprintf("%-*s", width, tool))+"  "+hitlDecisionStyle(decision).Render(hitlDecisionMark(decision)))
		}
	}
	fmt.Fprintln(out, styles.muted.Render("  ")+hitlAllowStyle.Render("✓")+styles.muted.Render(" allow   ")+hitlDenyStyle.Render("✗")+styles.muted.Render(" deny   ")+hitlHumanStyle.Render("i")+styles.muted.Render(" human"))
	return nil
}

func RunHITLPolicyAdd(ctx context.Context, client hitlPolicyManagementClient, in io.Reader, out io.Writer, name string) error {
	if name == auditor.DefaultHITLPolicyName {
		return hitlPolicyReadOnly(name)
	}
	output, err := client.ListHITLPolicies(ctx)
	if err != nil {
		return err
	}
	taken := make(map[string]bool, len(output.Policies))
	for _, policy := range output.Policies {
		taken[policy.Name] = true
	}
	if taken[name] {
		return fmt.Errorf("HITL policy %q already exists; choose another name", name)
	}
	policy, err := runHITLPolicyForm(ctx, in, out, name, taken)
	if err != nil {
		if errors.Is(err, errSelectionCanceled) {
			fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was saved."))
			return nil
		}
		return err
	}
	if err := client.CreateHITLPolicy(ctx, policy.Name, contracts.HITLPolicyConfig(policy.Decisions)); err != nil {
		return err
	}
	fmt.Fprintln(out, newHarnessStyles().active.Render("Added "+policy.Name))
	return nil
}

func RunHITLPolicyRemove(ctx context.Context, client hitlPolicyManagementClient, in io.Reader, out io.Writer, name string) error {
	if name == auditor.DefaultHITLPolicyName {
		return hitlPolicyReadOnly(name)
	}
	output, err := client.ListHITLPolicies(ctx)
	if err != nil {
		return err
	}
	names := []string{}
	for _, policy := range output.Policies {
		if policy.Name != auditor.DefaultHITLPolicyName {
			names = append(names, policy.Name)
		}
	}
	slices.Sort(names)
	if name == "" {
		if len(names) == 0 {
			return errors.New("no custom HITL policies configured")
		}
		name, err = runProviderMenu(ctx, in, out, "Remove HITL policy", "Select a policy", names)
		if err != nil {
			if errors.Is(err, errSelectionCanceled) {
				fmt.Fprintln(out, newHarnessStyles().muted.Render("Nothing was removed."))
				return nil
			}
			return err
		}
	} else if !slices.Contains(names, name) {
		return fmt.Errorf("HITL policy %q not found", name)
	}
	choice, err := runProviderMenu(ctx, in, out, "Remove HITL policy", "Remove "+name+"?", []string{"Keep it", "Remove it"})
	styles := newHarnessStyles()
	if errors.Is(err, errSelectionCanceled) || (err == nil && choice != "Remove it") {
		fmt.Fprintln(out, styles.muted.Render("Nothing was removed."))
		return nil
	}
	if err != nil {
		return err
	}
	if err := client.RemoveHITLPolicy(ctx, name); err != nil {
		return err
	}
	fmt.Fprintln(out, styles.active.Render("Removed "+name))
	fmt.Fprintln(out, styles.muted.Render(hitlPolicySessionNote))
	return nil
}
