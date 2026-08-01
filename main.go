package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/config"
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/tui"
)

func main() {
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()

	if *showVersion {
		fmt.Println("infai", config.Version())
		return
	}

	database, err := db.Open()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	service := backend.New(database)

	scanDirs, err := database.ListScanDirs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list scan dirs: %v\n", err)
		os.Exit(1)
	}

	syncResult, err := service.SyncModels(scanDirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		os.Exit(1)
	}
	for _, issue := range syncResult.Issues {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", issue.RootDir, issue.Error)
	}

	if theme, err := database.GetSetting("theme"); err == nil && theme != "" {
		tui.SetTheme(theme)
	}

	app := tui.NewApp(database, scanDirs, syncResult.Models, 80, 24)
	p := tea.NewProgram(&app, tea.WithAltScreen(), tea.WithMouseCellMotion())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigChan
		p.Quit()
	}()

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tui: %v\n", err)
		os.Exit(1)
	}
}
