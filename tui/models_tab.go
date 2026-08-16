package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dipankardas011/infai/backend"
	"github.com/dipankardas011/infai/model"
)

// ModelsTabModel is presentation for scan directory management.
// DB/scanner work is delegated to backend.Service.
type ModelsTabModel struct {
	service   *backend.Service
	dirs      []string
	models    []model.ModelEntry
	modelCnt  int
	cursor    int
	scrollOff int

	showInfo bool

	addingBrowse  bool
	fileBrowser   FileBrowserModel
	removeConfirm bool

	downloading   bool
	downloadModel DownloadModel

	syncing bool
	spinner spinner.Model

	errMsg string
	width  int
	height int
}

type modelsTabSyncDoneMsg struct {
	removed, updated int
	err              error
}

type modelsTabChangedMsg struct{}

func NewModelsTabModel(service *backend.Service, dirs []string, w, h int) ModelsTabModel {
	models, _ := service.ListModels()
	cp := make([]string, len(dirs))
	copy(cp, dirs)

	s := spinner.New()
	s.Spinner = spinner.Dot

	return ModelsTabModel{
		service:  service,
		dirs:     cp,
		models:   models,
		modelCnt: len(models),
		spinner:  s,
		width:    w,
		height:   h,
	}
}

func (m ModelsTabModel) SetSize(w, h int) ModelsTabModel {
	m.width = w
	m.height = h
	m.fileBrowser = m.fileBrowser.SetSize(w, h)
	m.downloadModel.width = w
	m.downloadModel.height = h
	return m
}

func (m *ModelsTabModel) Close() {}

// InModalInput reports whether keys currently belong to the file browser or a
// confirmation dialog rather than global shortcuts.
func (m ModelsTabModel) InModalInput() bool {
	return m.addingBrowse || m.removeConfirm || m.downloading
}

func (m ModelsTabModel) InFileBrowser() bool {
	return m.addingBrowse || (m.downloading && m.downloadModel.browsingDest)
}

func (m ModelsTabModel) Update(msg tea.Msg) (ModelsTabModel, tea.Cmd) {
	if m.downloading {
		if _, ok := msg.(downloadDoneMsg); ok {
			m.downloading = false
			models, _ := m.service.ListModels()
			m.models = models
			m.modelCnt = len(models)
			return m, func() tea.Msg { return modelsTabChangedMsg{} }
		}
		var cmd tea.Cmd
		m.downloadModel, cmd = m.downloadModel.Update(msg)
		return m, cmd
	}

	if m.removeConfirm {
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "y":
				m.removeConfirm = false
				if len(m.dirs) == 0 || m.cursor >= len(m.dirs) {
					return m, nil
				}
				path := m.dirs[m.cursor]
				if err := m.service.RemoveScanDir(path); err != nil {
					m.errMsg = styleError.Render(err.Error())
					return m, nil
				}
				m.dirs = append(m.dirs[:m.cursor], m.dirs[m.cursor+1:]...)
				if m.cursor >= len(m.dirs) && m.cursor > 0 {
					m.cursor--
				}
				models, _ := m.service.ListModels()
				m.modelCnt = len(models)
				m.errMsg = styleSuccess.Render("✓ removed")
				return m, func() tea.Msg { return modelsTabChangedMsg{} }
			case "n", "esc":
				m.removeConfirm = false
				m.errMsg = ""
				return m, nil
			}
		}
		return m, nil
	}

	if m.addingBrowse {
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
			m.addingBrowse = false
			if fm.Path == "" {
				return m, nil
			}
			for _, d := range m.dirs {
				if d == fm.Path {
					m.errMsg = styleError.Render("already in list")
					return m, nil
				}
			}
			result, err := m.service.ImportPath(fm.Path)
			if err != nil {
				m.errMsg = styleError.Render(err.Error())
				return m, nil
			}
			m.dirs = append(m.dirs, fm.Path)
			m.cursor = len(m.dirs) - 1
			msg := fmt.Sprintf("✓ imported %d model(s)", len(result.Models))
			if len(result.Issues) > 0 {
				msg += fmt.Sprintf(" (%d issues)", len(result.Issues))
			}
			m.errMsg = styleSuccess.Render(msg)
			models, _ := m.service.ListModels()
			m.modelCnt = len(models)
			return m, func() tea.Msg { return modelsTabChangedMsg{} }
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.dirs)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.dirs) > 0 && m.cursor < len(m.dirs) {
				m.showInfo = !m.showInfo
			}
		case "i":
			m.addingBrowse = true
			m.errMsg = ""
			home, _ := os.UserHomeDir()
			m.fileBrowser = NewFileBrowserModel()
			m.fileBrowser.currentDir = home
			m.fileBrowser.entries = loadDirEntries(home)
			m.fileBrowser = m.fileBrowser.SetSize(m.width, m.height)
			return m, nil
		case "w":
			m.downloading = true
			m.errMsg = ""
			m.downloadModel = NewDownloadModel(m.service, m.width, m.height)
			return m, textinput.Blink
		case "o":
			if len(m.dirs) == 0 || m.cursor >= len(m.dirs) {
				break
			}
			dir := m.dirs[m.cursor]
			for _, mdl := range m.models {
				if mdl.ScanDir == dir && mdl.SourceRepo != "" {
					url := "https://huggingface.co/" + mdl.SourceRepo
					if err := exec.Command("xdg-open", url).Start(); err != nil {
						m.errMsg = styleError.Render("could not open browser: " + err.Error())
					} else {
						m.errMsg = styleSuccess.Render("✓ opened in browser")
					}
					return m, nil
				}
			}
			m.errMsg = styleMuted.Render("no HF source for this folder")
			return m, nil
		case "d", "x", "delete":
			if len(m.dirs) == 0 || m.cursor >= len(m.dirs) {
				break
			}
			m.removeConfirm = true
			m.errMsg = ""
			return m, nil
		case "s":
			if m.syncing || len(m.dirs) == 0 {
				break
			}
			folders := append([]string(nil), m.dirs...)
			service := m.service
			m.syncing = true
			m.errMsg = ""
			return m, tea.Batch(
				m.spinner.Tick,
				func() tea.Msg {
					res, err := service.SyncModels(folders)
					return modelsTabSyncDoneMsg{removed: res.Removed, updated: res.Updated, err: err}
				},
			)
		}
	case modelsTabSyncDoneMsg:
		m.syncing = false
		if msg.err != nil {
			m.errMsg = styleError.Render(msg.err.Error())
		} else {
			m.errMsg = styleSuccess.Render(fmt.Sprintf("✓ synced: %d updated, %d removed", msg.updated, msg.removed))
			models, _ := m.service.ListModels()
			m.models = models
			m.modelCnt = len(models)
		}
	case spinner.TickMsg:
		if m.syncing {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	maxVisible := m.height - 8
	if maxVisible < 1 {
		maxVisible = 1
	}
	if m.cursor < m.scrollOff {
		m.scrollOff = m.cursor
	} else if m.cursor >= m.scrollOff+maxVisible {
		m.scrollOff = m.cursor - maxVisible + 1
	}
	if m.scrollOff < 0 {
		m.scrollOff = 0
	}

	return m, nil
}

func (m ModelsTabModel) View() string {
	t := ActiveTheme
	if m.downloading {
		return m.downloadModel.View()
	}
	if m.addingBrowse {
		return m.fileBrowser.View()
	}

	titleStyle := lipgloss.NewStyle().Foreground(t.Primary).Bold(true)
	mutedStyle := styleMuted
	successStyle := lipgloss.NewStyle().Foreground(t.Success)

	var sb strings.Builder

	if m.removeConfirm && m.cursor < len(m.dirs) {
		sb.WriteString(titleStyle.Render("Model Directories") + "\n\n")
		sb.WriteString(styleError.Render("  Remove this scan folder?") + "\n\n")
		sb.WriteString(mutedStyle.Render("  Folder: ") + successStyle.Render(m.dirs[m.cursor]) + "\n")
		sb.WriteString(mutedStyle.Render("  Models under it disappear from the list after the next sync.") + "\n\n")
		sb.WriteString(mutedStyle.Render("  y: confirm  n/esc: cancel") + "\n")
		boxW := max(m.width-4, 30)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Error).
			Padding(1, 2).
			Width(boxW).
			MaxHeight(max(m.height, 1)).
			Render(sb.String())
	}

	if m.showInfo && len(m.dirs) > 0 && m.cursor < len(m.dirs) {
		return m.splitView(t, titleStyle, mutedStyle)
	}

	sb.WriteString(titleStyle.Render("Model Directories") + "\n")
	if m.modelCnt > 0 {
		sb.WriteString(mutedStyle.Render(fmt.Sprintf("  %d models discovered\n", m.modelCnt)))
	}
	sb.WriteString("\n")

	maxVisible := max(m.height-10, 3)
	if len(m.dirs) == 0 {
		sb.WriteString(mutedStyle.Render("  No folders configured.") + "\n")
		sb.WriteString(mutedStyle.Render("  Press [i] to import or [w] to download.") + "\n")
	} else {
		end := m.scrollOff + maxVisible
		if end > len(m.dirs) {
			end = len(m.dirs)
		}
		for i := m.scrollOff; i < end; i++ {
			d := m.dirs[i]
			availW := max(m.width-10, 10)
			display := d
			if len(display) > availW {
				display = "…" + display[len(display)-(availW-3):]
			}
			if i == m.cursor {
				sb.WriteString(styleSelRow.Render(padToWidth("▶ "+display, availW+2)) + "\n")
			} else {
				sb.WriteString("  " + mutedStyle.Render(display) + "\n")
			}
		}
	}

	sb.WriteString("\n")
	if m.syncing {
		sb.WriteString(styleSelected.Render(m.spinner.View()+" syncing...") + "\n")
	} else if m.errMsg != "" {
		sb.WriteString(m.errMsg + "\n")
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

func (m ModelsTabModel) splitView(t Theme, titleStyle, mutedStyle lipgloss.Style) string {
	leftW := m.width * 2 / 5
	if leftW < 25 {
		leftW = 25
	}
	if leftW > m.width-30 {
		leftW = m.width - 30
	}
	rightW := m.width - leftW - 1
	panelH := max(m.height, 3)

	var left strings.Builder
	left.WriteString(titleStyle.Render("Directories") + "\n")
	innerH := panelH - 6
	if innerH < 1 {
		innerH = 1
	}
	end := m.scrollOff + innerH
	if end > len(m.dirs) {
		end = len(m.dirs)
	}
	innerLeftW := max(leftW-6, 10)
	for i := m.scrollOff; i < end; i++ {
		d := m.dirs[i]
		display := d
		if len(display) > innerLeftW {
			display = "…" + display[len(display)-(innerLeftW-3):]
		}
		if i == m.cursor {
			left.WriteString(styleSelRow.Render(padToWidth("▶ "+display, innerLeftW+2)) + "\n")
		} else {
			left.WriteString("  " + mutedStyle.Render(display) + "\n")
		}
	}
	if m.syncing {
		left.WriteString(styleSelected.Render(m.spinner.View()+" syncing...") + "\n")
	} else if m.errMsg != "" {
		left.WriteString(m.errMsg + "\n")
	}

	var right strings.Builder
	selectedDir := m.dirs[m.cursor]
	var dirModels []model.ModelEntry
	for _, mdl := range m.models {
		if mdl.ScanDir == selectedDir {
			dirModels = append(dirModels, mdl)
		}
	}

	right.WriteString(titleStyle.Render(fmt.Sprintf("Models (%d)", len(dirModels))) + "\n")

	if len(dirModels) == 0 {
		right.WriteString(mutedStyle.Render("  No models in this folder.") + "\n")
	} else {
		labelStyle := lipgloss.NewStyle().Foreground(t.Secondary).Bold(true)
		valStyle := lipgloss.NewStyle().Foreground(t.Text)
		innerRightW := max(rightW-6, 20)
		maxModels := max((panelH-6)/11, 1)

		for idx, mdl := range dirModels {
			if idx >= maxModels {
				right.WriteString(mutedStyle.Render(fmt.Sprintf("\n  +%d more...", len(dirModels)-maxModels)) + "\n")
				break
			}
			if idx > 0 {
				right.WriteString(mutedStyle.Render("  "+strings.Repeat("─", min(innerRightW-2, 40))) + "\n")
			}

			right.WriteString(lipgloss.NewStyle().Foreground(t.Primary).Bold(true).Render("  "+mdl.DisplayName) + "\n")

			typeBadge := string(mdl.Type)
			right.WriteString(labelStyle.Render("  Type: ") + valStyle.Render(typeBadge) + "\n")

			var meta model.ModelMetadata
			if mdl.Metadata != "" {
				_ = json.Unmarshal([]byte(mdl.Metadata), &meta)
			}

			if meta.Architecture != "" {
				right.WriteString(labelStyle.Render("  Arch: ") + valStyle.Render(meta.Architecture) + "\n")
			}
			if meta.ParameterCount > 0 {
				right.WriteString(labelStyle.Render("  Params: ") + valStyle.Render(formatParams(meta.ParameterCount)) + "\n")
			}
			if meta.Quantization != "" {
				right.WriteString(labelStyle.Render("  Quant: ") + valStyle.Render(meta.Quantization) + "\n")
			}
			if meta.ContextLength > 0 {
				right.WriteString(labelStyle.Render("  Context: ") + valStyle.Render(fmt.Sprintf("%d", meta.ContextLength)) + "\n")
			}
			if meta.BlockCount > 0 {
				right.WriteString(labelStyle.Render("  Layers: ") + valStyle.Render(fmt.Sprintf("%d", meta.BlockCount)) + "\n")
			}
			if attention := formatAttentionMetadata(meta); attention != "" {
				right.WriteString(labelStyle.Render("  Attention: ") + valStyle.Render(attention) + "\n")
			}
			if meta.NumExperts > 0 {
				moe := fmt.Sprintf("%d experts, %d active/token", meta.NumExperts, meta.NumExpertsPerToken)
				if meta.MoEExpertBytes > 0 {
					moe += ", " + formatFileSize(int64(meta.MoEExpertBytes)) + " expert tensors"
				}
				right.WriteString(labelStyle.Render("  MoE: ") + valStyle.Render(moe) + "\n")
			}
			if meta.MTPNumLayers > 0 {
				right.WriteString(labelStyle.Render("  MTP: ") + valStyle.Render(fmt.Sprintf("%d layer(s)", meta.MTPNumLayers)) + "\n")
			}
			if meta.FileSizeBytes > 0 {
				right.WriteString(labelStyle.Render("  Size: ") + valStyle.Render(formatFileSize(meta.FileSizeBytes)) + "\n")
			}
			if mdl.SourceRepo != "" {
				right.WriteString(labelStyle.Render("  HF Repo: ") + valStyle.Render(mdl.SourceRepo) + "\n")
				if mdl.SourceRevision != "" {
					right.WriteString(labelStyle.Render("  Revision: ") + valStyle.Render(truncateRunText(mdl.SourceRevision, max(innerRightW-14, 12))) + "\n")
				}
				if files := formatSourceFiles(mdl.SourceFiles); files != "" {
					right.WriteString(labelStyle.Render("  Files: ") + valStyle.Render(truncateRunText(files, max(innerRightW-10, 12))) + "\n")
				}
			}
		}
	}

	leftBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Primary).
		Padding(0, 1).
		Width(leftW - 2).
		Height(panelH - 2).
		MaxHeight(panelH).
		Render(left.String())

	rightBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Muted).
		Padding(0, 1).
		Width(rightW - 2).
		Height(panelH - 2).
		MaxHeight(panelH).
		Render(right.String())

	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func formatAttentionMetadata(meta model.ModelMetadata) string {
	if len(meta.AttentionLayerTypes) > 0 {
		full, sliding := 0, 0
		for _, layerType := range meta.AttentionLayerTypes {
			switch layerType {
			case "full_attention":
				full++
			case "sliding_attention":
				sliding++
			}
		}
		if full > 0 || sliding > 0 {
			return fmt.Sprintf("%d full, %d sliding", full, sliding)
		}
	}
	if meta.SlidingWindow > 0 {
		return fmt.Sprintf("sliding window %d", meta.SlidingWindow)
	}
	if meta.BlockCount > 0 {
		return "full-context"
	}
	return ""
}

func formatSourceFiles(raw string) string {
	if raw == "" {
		return ""
	}
	var files []string
	if json.Unmarshal([]byte(raw), &files) != nil {
		return raw
	}
	return strings.Join(files, ", ")
}

func formatParams(n uint64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatFileSize(b int64) string {
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
