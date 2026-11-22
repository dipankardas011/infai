package main

import (
	"github.com/dipankardas011/infai/pkg/agent/store"
	"github.com/dipankardas011/infai/pkg/agent/tui"
	"github.com/spf13/cobra"
)

func newBrowserCmd() *cobra.Command {
	browser := &cobra.Command{
		Use:   "browser",
		Short: "inspect the infai Chrome extension and the tabs it drives",
		Args:  cobra.NoArgs,
	}
	addServerFlags(browser)
	browser.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "show the extension connection, session tabs, and Chrome profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			extDir, err := store.ExtensionDir()
			if err != nil {
				return err
			}
			return tui.RunBrowserStatus(cmd.Context(), remoteClient(), cmd.OutOrStdout(), extDir)
		},
	})
	return browser
}
