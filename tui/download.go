package tui

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/downloader"
	"github.com/dipankardas011/infai/hub"
	"github.com/dipankardas011/infai/model"
)

type downloadStep int

const (
	stepSearch downloadStep = iota
	stepSelectRepo
	stepSelectEngine
	stepSelectVariant
	stepReviewPlan
	stepChooseDest
	stepDownloading
	stepDone
)

const searchPageSize = 20

type downloadSearchDoneMsg struct {
	results []hub.ModelInfo
	err     error
	append  bool
}

type downloadFilesDoneMsg struct {
	files    []hub.FileEntry
	variants []downloader.GGUFVariant
	plan     *downloader.DownloadPlan
	engine   model.EngineKind
	err      error
}

type downloadProgressMsg struct {
	progress downloader.OverallProgress
	done     bool
}

type downloadImportDoneMsg struct {
	result *backend.ImportResult
	err    error
}

type downloadDoneMsg struct{}

type DownloadModel struct {
	service *backend.Service
	hub     *hub.Client
	dl      *downloader.Downloader
	step    downloadStep
	width   int
	height  int

	searchInput textinput.Model
	searching   bool
	spinner     spinner.Model

	results     []hub.ModelInfo
	repoCursor  int
	repoScroll  int
	searchQuery string
	hasMore     bool
	loadingMore bool

	selectedRepo *hub.ModelInfo
	engineCursor int
	engineKind   model.EngineKind

	variants      []downloader.GGUFVariant
	variantCursor int
	variantScroll int

	files      []hub.FileEntry
	plan       *downloader.DownloadPlan
	planScroll int

	fileBrowser  FileBrowserModel
	browsingDest bool
	destPath     string

	progress   *downloader.OverallProgress
	progressCh <-chan downloader.OverallProgress
	cancelFunc context.CancelFunc

	importResult *backend.ImportResult

	errMsg string
}

func NewDownloadModel(service *backend.Service, w, h int) DownloadModel {
	ti := textinput.New()
	ti.Placeholder = "model name or author/name"
	ti.CharLimit = 256
	ti.Focus()

	s := spinner.New()
	s.Spinner = spinner.Dot

	token := hub.TokenFromEnv()
	dl := downloader.NewDownloader(downloader.Config{})
	if token != "" {
		dl.SetToken(token)
	}

	return DownloadModel{
		service:     service,
		hub:         hub.NewClient(token),
		dl:          dl,
		step:        stepSearch,
		width:       w,
		height:      h,
		searchInput: ti,
		spinner:     s,
	}
}

func (m DownloadModel) InModalInput() bool {
	return m.browsingDest || m.step == stepSearch
}

func (m DownloadModel) Update(msg tea.Msg) (DownloadModel, tea.Cmd) {
	switch m.step {
	case stepSearch:
		return m.updateSearch(msg)
	case stepSelectRepo:
		return m.updateSelectRepo(msg)
	case stepSelectEngine:
		return m.updateSelectEngine(msg)
	case stepSelectVariant:
		return m.updateSelectVariant(msg)
	case stepReviewPlan:
		return m.updateReviewPlan(msg)
	case stepChooseDest:
		return m.updateChooseDest(msg)
	case stepDownloading:
		return m.updateDownloading(msg)
	case stepDone:
		return m.updateDone(msg)
	}
	return m, nil
}

func (m DownloadModel) updateSearch(msg tea.Msg) (DownloadModel, tea.Cmd) {
	if m.searching {
		switch msg := msg.(type) {
		case downloadSearchDoneMsg:
			m.searching = false
			if msg.err != nil {
				m.errMsg = msg.err.Error()
				return m, nil
			}
			if len(msg.results) == 0 && len(m.results) == 0 {
				m.errMsg = "no results found"
				return m, nil
			}
			if msg.append {
				m.loadingMore = false
				m.results = append(m.results, msg.results...)
			} else {
				m.results = msg.results
				m.repoCursor = 0
				m.repoScroll = 0
			}
			m.hasMore = len(msg.results) == searchPageSize
			m.step = stepSelectRepo
			m.errMsg = ""
			return m, nil
		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			query := strings.TrimSpace(m.searchInput.Value())
			if query == "" {
				return m, nil
			}
			m.searching = true
			m.searchQuery = query
			m.results = nil
			m.errMsg = ""
			hubClient := m.hub
			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					results, err := hubClient.Search(context.Background(), hub.SearchParams{
						Query:     query,
						Limit:     searchPageSize,
						Sort:      "downloads",
						Direction: "-1",
					})
					return downloadSearchDoneMsg{results: results, err: err}
				},
			)
		case "esc":
			return m, func() tea.Msg { return downloadDoneMsg{} }
		}
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	return m, cmd
}

func (m DownloadModel) updateSelectRepo(msg tea.Msg) (DownloadModel, tea.Cmd) {
	if m.loadingMore {
		switch msg := msg.(type) {
		case downloadSearchDoneMsg:
			return m.updateSearch(msg)
		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.repoCursor > 0 {
				m.repoCursor--
			}
		case "down", "j":
			if m.repoCursor < len(m.results)-1 {
				m.repoCursor++
			}
		case "l":
			if m.hasMore {
				return m.loadMore()
			}
		case "enter":
			repo := m.results[m.repoCursor]
			m.selectedRepo = &repo
			m.engineCursor = 0
			m.step = stepSelectEngine
			m.errMsg = ""
		case "o":
			if m.repoCursor < len(m.results) {
				url := "https://huggingface.co/" + m.results[m.repoCursor].ID
				_ = exec.Command("xdg-open", url).Start()
			}
		case "esc":
			m.step = stepSearch
			m.errMsg = ""
		}
	}

	m.repoScroll, _ = m.clampRepoScroll()
	return m, nil
}

func (m DownloadModel) repoMaxVisible() int {
	// box: border(2) + padding(2) = 4
	// inner chrome: title(1) + ↑(1) + ↓(1) + status(1) + hint(1) = 5
	return max(m.height-4-5, 3)
}

func (m DownloadModel) updateSelectEngine(msg tea.Msg) (DownloadModel, tea.Cmd) {
	if m.searching {
		switch msg := msg.(type) {
		case downloadFilesDoneMsg:
			m.searching = false
			if msg.err != nil {
				m.errMsg = msg.err.Error()
				return m, nil
			}
			m.files = msg.files
			m.engineKind = msg.engine
			if msg.engine == model.EngineLlamaCPP && len(msg.variants) > 1 {
				m.variants = msg.variants
				m.variantCursor = 0
				m.variantScroll = 0
				m.step = stepSelectVariant
			} else if msg.plan != nil {
				m.plan = msg.plan
				m.step = stepReviewPlan
			} else if msg.engine == model.EngineLlamaCPP && len(msg.variants) == 1 {
				m.plan = downloader.PlanGGUFVariant(m.selectedRepo.ID, "main", msg.variants[0], msg.files)
				m.step = stepReviewPlan
			}
			m.errMsg = ""
			return m, nil
		case spinner.TickMsg:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.engineCursor > 0 {
				m.engineCursor--
			}
		case "down", "j":
			if m.engineCursor < 1 {
				m.engineCursor++
			}
		case "enter":
			engineKind := model.EngineLlamaCPP
			if m.engineCursor == 1 {
				engineKind = model.EngineVLLM
			}
			m.searching = true
			m.errMsg = ""
			hubClient := m.hub
			repoID := m.selectedRepo.ID
			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					files, err := hubClient.ListFiles(context.Background(), repoID, "main")
					if err != nil {
						return downloadFilesDoneMsg{err: err}
					}
					if engineKind == model.EngineLlamaCPP {
						variants := downloader.ListGGUFVariants(files)
						if len(variants) == 0 {
							return downloadFilesDoneMsg{err: fmt.Errorf("no GGUF files found in repository")}
						}
						return downloadFilesDoneMsg{files: files, variants: variants, engine: engineKind}
					}
					plan, err := downloader.PlanFiles(repoID, "main", files, engineKind)
					if err != nil {
						return downloadFilesDoneMsg{err: err}
					}
					return downloadFilesDoneMsg{files: files, plan: plan, engine: engineKind}
				},
			)
		case "esc":
			m.step = stepSelectRepo
			m.errMsg = ""
		}
	}
	return m, nil
}

func (m DownloadModel) updateSelectVariant(msg tea.Msg) (DownloadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.variantCursor > 0 {
				m.variantCursor--
			}
		case "down", "j":
			if m.variantCursor < len(m.variants)-1 {
				m.variantCursor++
			}
		case "enter":
			selected := m.variants[m.variantCursor]
			m.plan = downloader.PlanGGUFVariant(m.selectedRepo.ID, "main", selected, m.files)
			m.planScroll = 0
			m.step = stepReviewPlan
			m.errMsg = ""
		case "esc":
			m.step = stepSelectEngine
			m.errMsg = ""
		}
	}

	maxVisible := m.variantMaxVisible()
	if m.variantCursor < m.variantScroll {
		m.variantScroll = m.variantCursor
	} else if m.variantCursor >= m.variantScroll+maxVisible {
		m.variantScroll = m.variantCursor - maxVisible + 1
	}

	return m, nil
}

func (m DownloadModel) variantMaxVisible() int {
	// box: border(2) + padding(2) = 4
	// inner chrome: title(1) + repo(1) + hint(1) = 3
	return max(m.height-4-3, 3)
}

func (m DownloadModel) updateReviewPlan(msg tea.Msg) (DownloadModel, tea.Cmd) {
	totalFiles := len(m.plan.Files) + len(m.plan.OptionalFiles)
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.planScroll > 0 {
				m.planScroll--
			}
		case "down", "j":
			maxVisible := m.planMaxVisible()
			if m.planScroll < totalFiles-maxVisible {
				m.planScroll++
			}
		case "enter":
			m.browsingDest = true
			m.errMsg = ""
			home, _ := filepath.Abs(".")
			m.fileBrowser = NewFileBrowserModel()
			m.fileBrowser.currentDir = home
			m.fileBrowser.entries = loadDirEntries(home)
			m.fileBrowser = m.fileBrowser.SetSize(m.width, m.height)
			m.step = stepChooseDest
		case "esc":
			m.step = stepSelectEngine
			m.errMsg = ""
		}
	}
	if m.planScroll < 0 {
		m.planScroll = 0
	}
	return m, nil
}

func (m DownloadModel) loadMore() (DownloadModel, tea.Cmd) {
	m.loadingMore = true
	m.searching = true
	offset := len(m.results)
	query := m.searchQuery
	hubClient := m.hub
	return m, tea.Batch(
		m.spinner.Tick,
		func() tea.Msg {
			results, err := hubClient.Search(context.Background(), hub.SearchParams{
				Query:     query,
				Limit:     searchPageSize,
				Offset:    offset,
				Sort:      "downloads",
				Direction: "-1",
			})
			return downloadSearchDoneMsg{results: results, err: err, append: true}
		},
	)
}

func (m DownloadModel) planMaxVisible() int {
	// box: border(2) + padding(2) = 4
	// inner chrome: title(1) + repo(1) + ↑(1) + ↓(1) + total(1) + hint(1) = 6
	return max(m.height-4-6, 3)
}

func (m DownloadModel) updateChooseDest(msg tea.Msg) (DownloadModel, tea.Cmd) {
	var cmd tea.Cmd
	m.fileBrowser, cmd = m.fileBrowser.Update(msg)

	if _, ok := msg.(tea.KeyMsg); ok {
		switch msg.(type) {
		case FileBrowserSavedMsg:
		default:
			return m, cmd
		}
	}

	if fm, ok := msg.(FileBrowserSavedMsg); ok {
		m.browsingDest = false
		if fm.Path == "" {
			m.step = stepReviewPlan
			return m, nil
		}
		m.destPath = fm.Path
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelFunc = cancel

		ch, err := m.dl.Download(ctx, m.plan, m.destPath)
		if err != nil {
			cancel()
			m.errMsg = err.Error()
			m.step = stepReviewPlan
			return m, nil
		}
		m.progressCh = ch
		m.step = stepDownloading
		m.errMsg = ""
		return m, waitForProgress(ch)
	}
	return m, cmd
}

func (m DownloadModel) updateDownloading(msg tea.Msg) (DownloadModel, tea.Cmd) {
	switch msg := msg.(type) {
	case downloadProgressMsg:
		if msg.done {
			if m.progress != nil && m.progress.State == downloader.FileFailed {
				m.errMsg = "download failed"
				for _, f := range m.progress.Files {
					if f.Error != nil {
						m.errMsg = f.Error.Error()
						break
					}
				}
				m.step = stepDone
				return m, nil
			}
			service := m.service
			dest := m.destPath
			plan := m.plan
			return m, func() tea.Msg {
				result, err := service.ImportDownloaded(dest, plan)
				return downloadImportDoneMsg{result: &result, err: err}
			}
		}
		m.progress = &msg.progress
		return m, waitForProgress(m.progressCh)

	case downloadImportDoneMsg:
		m.importResult = msg.result
		if msg.err != nil {
			m.errMsg = msg.err.Error()
		}
		m.step = stepDone
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			if m.cancelFunc != nil {
				m.cancelFunc()
			}
			m.errMsg = "cancelled"
			m.step = stepDone
			return m, nil
		}
	}
	return m, nil
}

func (m DownloadModel) updateDone(msg tea.Msg) (DownloadModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "enter", "esc":
			return m, func() tea.Msg { return downloadDoneMsg{} }
		}
	}
	return m, nil
}

func waitForProgress(ch <-chan downloader.OverallProgress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return downloadProgressMsg{done: true}
		}
		return downloadProgressMsg{progress: p}
	}
}

func (m DownloadModel) clampRepoScroll() (int, int) {
	maxVisible := m.repoMaxVisible()
	cursor := m.repoCursor
	scroll := m.repoScroll
	if cursor < scroll {
		scroll = cursor
	} else if cursor >= scroll+maxVisible {
		scroll = cursor - maxVisible + 1
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll, maxVisible
}

func (m DownloadModel) View() string {
	t := ActiveTheme
	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := styleMuted

	var sb strings.Builder

	switch m.step {
	case stepSearch:
		sb.WriteString(titleStyle.Render("Download from Hugging Face") + "\n\n")
		sb.WriteString("  " + m.searchInput.View() + "\n\n")
		if m.searching {
			sb.WriteString("  " + m.spinner.View() + " searching...\n")
		}
		if m.errMsg != "" {
			sb.WriteString("  " + styleError.Render(m.errMsg) + "\n")
		}
		sb.WriteString("\n" + mutedStyle.Render("  enter: search  esc: cancel"))

	case stepSelectRepo:
		sb.WriteString(titleStyle.Render("Select Repository") + "\n")
		scroll, maxVisible := m.clampRepoScroll()
		end := scroll + maxVisible
		if end > len(m.results) {
			end = len(m.results)
		}
		hasAbove := scroll > 0
		hasBelow := end < len(m.results)
		if hasAbove {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ %d more", scroll)) + "\n")
		}
		for i := scroll; i < end; i++ {
			r := m.results[i]
			dl := formatDownloads(r.Downloads)
			line := fmt.Sprintf("%s  ↓%s  ♥%d", r.ID, dl, r.Likes)
			availW := max(m.width-10, 20)
			if len(line) > availW {
				line = line[:availW-1] + "…"
			}
			if i == m.repoCursor {
				sb.WriteString(styleSelRow.Render(padToWidth("▶ "+line, availW+2)) + "\n")
			} else {
				sb.WriteString("  " + mutedStyle.Render(line) + "\n")
			}
		}
		if hasBelow {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.results)-end)) + "\n")
		}
		if m.loadingMore {
			sb.WriteString(m.spinner.View() + " loading more...\n")
		} else if m.hasMore {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  %d results  l: load more", len(m.results))) + "\n")
		}
		sb.WriteString(mutedStyle.Render("  enter: select  o: open  esc: back"))

	case stepSelectEngine:
		sb.WriteString(titleStyle.Render("Select Engine") + "\n")
		sb.WriteString(mutedStyle.Render("  "+m.selectedRepo.ID) + "\n\n")
		if m.searching {
			sb.WriteString("  " + m.spinner.View() + " planning download...\n")
		} else {
			engines := []string{"llama.cpp (GGUF)", "vLLM (safetensors)"}
			for i, e := range engines {
				if i == m.engineCursor {
					sb.WriteString(styleSelRow.Render(padToWidth("▶ "+e, max(m.width-10, 20))) + "\n")
				} else {
					sb.WriteString("  " + mutedStyle.Render(e) + "\n")
				}
			}
		}
		if m.errMsg != "" {
			sb.WriteString("\n  " + styleError.Render(m.errMsg) + "\n")
		}
		sb.WriteString("\n" + mutedStyle.Render("  enter: select  esc: back"))

	case stepSelectVariant:
		sb.WriteString(titleStyle.Render("Select Quantization") + "\n")
		sb.WriteString(mutedStyle.Render("  "+m.selectedRepo.ID) + "\n")
		maxVisible := m.variantMaxVisible()
		end := m.variantScroll + maxVisible
		if end > len(m.variants) {
			end = len(m.variants)
		}
		if m.variantScroll > 0 {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ %d more", m.variantScroll)) + "\n")
		}
		for i := m.variantScroll; i < end; i++ {
			v := m.variants[i]
			sizeStr := dlFormatBytes(v.TotalBytes)
			label := v.Name
			if v.Sharded {
				label += fmt.Sprintf(" (%d shards)", v.ShardCount)
			}
			line := fmt.Sprintf("%-50s %s", label, sizeStr)
			availW := max(m.width-10, 20)
			if len(line) > availW {
				line = line[:availW-1] + "…"
			}
			if i == m.variantCursor {
				sb.WriteString(styleSelRow.Render(padToWidth("▶ "+line, availW+2)) + "\n")
			} else {
				sb.WriteString("  " + mutedStyle.Render(line) + "\n")
			}
		}
		if end < len(m.variants) {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(m.variants)-end)) + "\n")
		}
		sb.WriteString(mutedStyle.Render("  enter: select  esc: back"))

	case stepReviewPlan:
		sb.WriteString(titleStyle.Render("Download Plan") + "\n")
		sb.WriteString(mutedStyle.Render("  "+m.plan.RepoID) + "\n")

		allFiles := make([]downloader.PlanFile, 0, len(m.plan.Files)+len(m.plan.OptionalFiles))
		allFiles = append(allFiles, m.plan.Files...)
		optStart := len(m.plan.Files)
		allFiles = append(allFiles, m.plan.OptionalFiles...)

		maxVisible := m.planMaxVisible()
		start := m.planScroll
		end := start + maxVisible
		if end > len(allFiles) {
			end = len(allFiles)
		}
		optLabelVisible := len(m.plan.OptionalFiles) > 0 && optStart >= start && optStart < end
		effectiveMax := maxVisible
		if optLabelVisible {
			effectiveMax--
			end = start + effectiveMax
			if end > len(allFiles) {
				end = len(allFiles)
			}
		}
		if start > 0 {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↑ %d more", start)) + "\n")
		}
		for i := start; i < end; i++ {
			if i == optStart && len(m.plan.OptionalFiles) > 0 {
				sb.WriteString(mutedStyle.Render("  Optional:") + "\n")
			}
			f := allFiles[i]
			sb.WriteString(fmt.Sprintf("  %-50s %s\n", truncate(f.Path, 50), dlFormatBytes(f.Size)))
		}
		if end < len(allFiles) {
			sb.WriteString(mutedStyle.Render(fmt.Sprintf("  ↓ %d more", len(allFiles)-end)) + "\n")
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(t.Secondary).Bold(true).Render(
			fmt.Sprintf("  Total: %s  (%d files)", dlFormatBytes(m.plan.TotalBytes), len(allFiles)),
		) + "\n")
		sb.WriteString(mutedStyle.Render("  ↑/↓: scroll  enter: destination  esc: back"))

	case stepChooseDest:
		if m.browsingDest {
			return m.fileBrowser.View()
		}

	case stepDownloading:
		sb.WriteString(titleStyle.Render("Downloading") + "\n")
		sb.WriteString(mutedStyle.Render("  "+m.plan.RepoID+" → "+m.destPath) + "\n\n")
		if m.progress != nil {
			pct := 0
			if m.progress.TotalBytes > 0 {
				pct = int(m.progress.DoneBytes * 100 / m.progress.TotalBytes)
			}
			barW := max(m.width-30, 10)
			filled := barW * pct / 100
			bar := strings.Repeat("█", filled) + strings.Repeat("░", barW-filled)
			sb.WriteString(fmt.Sprintf("  [%s] %d%%  %s / %s\n\n",
				bar, pct,
				dlFormatBytes(m.progress.DoneBytes),
				dlFormatBytes(m.progress.TotalBytes),
			))
			for _, f := range m.progress.Files {
				icon := "  "
				switch f.State {
				case downloader.FileCompleted:
					icon = styleSuccess.Render("✓ ")
				case downloader.FileDownloading, downloader.FileResuming:
					icon = styleSelected.Render("↓ ")
				case downloader.FileVerifying:
					icon = styleSelected.Render("⋯ ")
				case downloader.FileFailed:
					icon = styleError.Render("✗ ")
				case downloader.FilePending:
					icon = mutedStyle.Render("· ")
				}
				name := filepath.Base(f.Path)
				if len(name) > 40 {
					name = name[:37] + "..."
				}
				filePct := ""
				if f.Total > 0 && f.State == downloader.FileDownloading {
					filePct = fmt.Sprintf(" %d%%", f.Downloaded*100/f.Total)
				}
				sb.WriteString(fmt.Sprintf("  %s%-40s %s%s\n", icon, name, dlFormatBytes(f.Total), filePct))
			}
		} else {
			sb.WriteString("  " + m.spinner.View() + " starting...\n")
		}
		sb.WriteString("\n" + mutedStyle.Render("  esc: cancel download"))

	case stepDone:
		sb.WriteString(titleStyle.Render("Download Complete") + "\n\n")
		if m.errMsg != "" {
			sb.WriteString("  " + styleError.Render(m.errMsg) + "\n\n")
		} else if m.importResult != nil {
			sb.WriteString(styleSuccess.Render(fmt.Sprintf("  ✓ %d model(s) imported", len(m.importResult.Models))) + "\n")
			if len(m.importResult.Issues) > 0 {
				for _, issue := range m.importResult.Issues {
					sb.WriteString("  " + styleWarning.Render("! "+issue) + "\n")
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString(mutedStyle.Render("  enter/esc: done"))
	}

	boxW := max(m.width-4, 30)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(1, 2).
		Width(boxW).
		MaxHeight(max(m.height, 1)).
		Render(sb.String())
}

func dlFormatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func formatDownloads(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
