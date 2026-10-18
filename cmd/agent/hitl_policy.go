package main

import (
	"github.com/dipankardas011/infai/pkg/agent/tui"
	"github.com/spf13/cobra"
)

func newHITLPolicyCmd() *cobra.Command {
	policy := &cobra.Command{
		Use:   "hitl_policy",
		Short: "manage human-in-the-loop tool policies",
		Args:  cobra.NoArgs,
	}
	addServerFlags(policy)
	policy.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "list HITL policies and their tool decisions",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return tui.RunHITLPolicyList(cmd.Context(), remoteClient(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "add [name]",
			Short: "create a custom HITL policy interactively",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tui.RunHITLPolicyAdd(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout(), optionalName(args))
			},
		},
		&cobra.Command{
			Use:   "remove [name]",
			Short: "remove a custom HITL policy after confirmation",
			Args:  cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return tui.RunHITLPolicyRemove(cmd.Context(), remoteClient(), cmd.InOrStdin(), cmd.OutOrStdout(), optionalName(args))
			},
		},
	)
	return policy
}
