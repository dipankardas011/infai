package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/internal/backend"
	"github.com/dipankardas011/infai/internal/config"
	"github.com/dipankardas011/infai/internal/db"
	"github.com/dipankardas011/infai/internal/tui"
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
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	service := backend.New(database)

	scanDirs, err := database.ListScanDirs()
	if err != nil {
		slog.Error("list scan dirs", "error", err)
		os.Exit(1)
	}

	syncResult, err := service.SyncModels(scanDirs)
	if err != nil {
		slog.Error("sync models", "error", err)
		os.Exit(1)
	}
	for _, issue := range syncResult.Issues {
		slog.Warn("scan issue", "root", issue.RootDir, "error", issue.Error)
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
		slog.Error("tui", "error", err)
		os.Exit(1)
	}
}
