package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

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

	app := tui.NewApp(database, nil, nil, 80, 24)
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
