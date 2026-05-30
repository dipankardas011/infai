package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dipankardas011/infai/config"
	"github.com/dipankardas011/infai/db"
	"github.com/dipankardas011/infai/scanner"
	"github.com/dipankardas011/infai/tui"
)

func main() {
	showVersion := flag.Bool("version", false, "print version")
	profileName := flag.String("p", "", "profile name to launch")
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

	scanDirs, err := database.ListScanDirs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list scan dirs: %v\n", err)
		os.Exit(1)
	}

	entries, err := scanner.Scan(scanDirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan: %v\n", err)
		os.Exit(1)
	}
	for i := range entries {
		if err := database.UpsertModel(&entries[i]); err != nil {
			fmt.Fprintf(os.Stderr, "upsert model: %v\n", err)
			os.Exit(1)
		}
	}

	if theme, err := database.GetSetting("theme"); err == nil && theme != "" {
		tui.SetTheme(theme)
	}

	serverBin, err := database.GetDefaultExecutorPath()
	if err != nil || serverBin == "" {
		if path, err := exec.LookPath("llama-server"); err == nil {
			serverBin = path
			_ = database.UpsertExecutor(db.Executor{
				ID:        "llamacpp",
				Path:      path,
				IsDefault: true,
			})
		}
	}

	app, err := tui.NewApp(database, serverBin, scanDirs, entries, 80, 24, *profileName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())

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
