// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

const (
	skyUseTitle       = "Sky Use"
	skyUsePlaceholder = "Paste sky use command (e.g., sky use cluster namespace service)"
	skyUseDialogKey   = "sky-use-dialog"
	skyUseHistoryFile = "sky_use_history.json"
	maxHistorySize    = 10
)

// SkyUseHistory manages the history of sky use commands
type SkyUseHistory struct {
	Commands []string `json:"commands"`
}

// LoadSkyUseHistory loads the command history from disk
func LoadSkyUseHistory() *SkyUseHistory {
	h := &SkyUseHistory{
		Commands: make([]string, 0, maxHistorySize),
	}

	appDir := os.Getenv("K9S_CONFIG_DIR")
	if appDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return h
		}
		appDir = filepath.Join(homeDir, ".config", "k9s")
	}

	historyPath := filepath.Join(appDir, skyUseHistoryFile)
	data, err := os.ReadFile(historyPath)
	if err != nil {
		// File doesn't exist yet, that's okay
		return h
	}

	if err := json.Unmarshal(data, h); err != nil {
		slog.Warn("Failed to parse sky use history", slogs.Error, err)
		return &SkyUseHistory{Commands: make([]string, 0, maxHistorySize)}
	}

	return h
}

// Save saves the command history to disk
func (h *SkyUseHistory) Save() error {
	appDir := os.Getenv("K9S_CONFIG_DIR")
	if appDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		appDir = filepath.Join(homeDir, ".config", "k9s")
	}

	// Ensure directory exists
	if err := os.MkdirAll(appDir, 0755); err != nil {
		return err
	}

	historyPath := filepath.Join(appDir, skyUseHistoryFile)
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(historyPath, data, 0644)
}

// Add adds a command to the history (most recent first)
// Only stores the parsed values (cluster namespace service), not "sky use"
func (h *SkyUseHistory) Add(command string) {
	// Parse the command to normalize it (remove "sky use" prefix)
	cluster, namespace, service, err := parseSkyUseCommand(command)
	if err != nil {
		// Invalid command, don't add to history
		return
	}

	// Store normalized format: just "cluster namespace service"
	normalizedCmd := fmt.Sprintf("%s %s %s", cluster, namespace, service)

	// Remove duplicate if it exists
	for i, cmd := range h.Commands {
		if cmd == normalizedCmd {
			h.Commands = append(h.Commands[:i], h.Commands[i+1:]...)
			break
		}
	}

	// Add to the front
	h.Commands = append([]string{normalizedCmd}, h.Commands...)

	// Trim to max size
	if len(h.Commands) > maxHistorySize {
		h.Commands = h.Commands[:maxHistorySize]
	}
}

// ShowSkyUseDialog presents a dialog for sky use command.
func ShowSkyUseDialog(app *App) {
	// Load history
	history := LoadSkyUseHistory()

	// Create main container
	flex := tview.NewFlex()
	flex.SetDirection(tview.FlexRow)
	flex.SetBorder(true)
	flex.SetTitle(" Sky Use ")
	flex.SetBorderColor(app.Styles.Frame().Border.FocusColor.Color())
	flex.SetBackgroundColor(app.Styles.BgColor())

	// Create instruction text
	instructions := tview.NewTextView()
	instructions.SetDynamicColors(true)
	instructions.SetTextAlign(tview.AlignCenter)
	instructions.SetBackgroundColor(app.Styles.BgColor())
	instructions.SetTextColor(app.Styles.FgColor())
	instructions.SetText("Paste sky command to switch environment")

	// Create input field
	input := tview.NewInputField()
	input.SetLabel("Command: ")
	input.SetPlaceholder("sky use <cluster> <namespace> <service>")
	input.SetFieldWidth(0)
	input.SetBackgroundColor(app.Styles.BgColor())
	input.SetLabelColor(app.Styles.FgColor())
	input.SetFieldTextColor(app.Styles.FgColor())
	input.SetFieldBackgroundColor(app.Styles.BgColor())
	input.SetPlaceholderTextColor(tcell.ColorGray)

	// Prevent newlines from auto-triggering Enter
	input.SetAcceptanceFunc(func(textToCheck string, lastChar rune) bool {
		return lastChar != '\n' && lastChar != '\r'
	})

	// Create list for history
	list := tview.NewList()
	list.SetBackgroundColor(app.Styles.BgColor())
	list.SetMainTextColor(app.Styles.FgColor())
	list.SetSecondaryTextColor(tcell.ColorGray)
	list.SetSelectedBackgroundColor(app.Styles.Frame().Menu.FgColor.Color())
	list.SetSelectedTextColor(app.Styles.BgColor())
	list.ShowSecondaryText(false)

	// Populate list with history
	if len(history.Commands) > 0 {
		for _, cmd := range history.Commands {
			cmdCopy := cmd // Capture for closure
			// Commands are stored as "cluster namespace service"
			parts := strings.Fields(cmd)
			if len(parts) >= 3 {
				// Align columns by padding cluster and namespace to fixed widths
				cluster := parts[0]
				namespace := parts[1]
				service := parts[2]

				// Pad cluster to 25 chars, namespace to 35 chars
				displayText := fmt.Sprintf("%-25s  %-35s  %s", cluster, namespace, service)

				list.AddItem(displayText, "", 0, func() {
					// Execute this command
					history.Add(cmdCopy)
					if err := history.Save(); err != nil {
						slog.Warn("Failed to save sky use history", slogs.Error, err)
					}
					handleSkyUse(app, cmdCopy)
					dismissSkyUseDialog(app)
				})
			}
		}
	} else {
		list.AddItem("No recent commands", "", 0, nil)
	}

	// Create help text
	helpText := tview.NewTextView()
	helpText.SetDynamicColors(true)
	helpText.SetTextAlign(tview.AlignCenter)
	helpText.SetBackgroundColor(app.Styles.BgColor())
	helpText.SetTextColor(tcell.ColorGray)
	helpText.SetText("[yellow::b]↑/↓[-:-:-] Navigate  [yellow::b]Enter[-:-:-] Select  [yellow::b]Tab[-:-:-] Switch  [yellow::b]Esc[-:-:-] Cancel")

	// Handle input field events
	input.SetInputCapture(func(evt *tcell.EventKey) *tcell.EventKey {
		switch evt.Key() {
		case tcell.KeyEscape:
			dismissSkyUseDialog(app)
			return nil
		case tcell.KeyEnter:
			text := input.GetText()
			if text != "" {
				history.Add(text)
				if err := history.Save(); err != nil {
					slog.Warn("Failed to save sky use history", slogs.Error, err)
				}
				handleSkyUse(app, text)
				dismissSkyUseDialog(app)
			}
			return nil
		case tcell.KeyDown:
			// Move focus to list
			if len(history.Commands) > 0 {
				app.SetFocus(list)
			}
			return nil
		case tcell.KeyTab:
			// Move focus to list
			if len(history.Commands) > 0 {
				app.SetFocus(list)
			}
			return nil
		case tcell.KeyRune:
			// Allow all runes (letters, numbers, special chars) to be typed
			// This includes '-', '_', etc.
			return evt
		case tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyDelete:
			// Allow deletion
			return evt
		case tcell.KeyLeft, tcell.KeyRight, tcell.KeyHome, tcell.KeyEnd:
			// Allow cursor movement
			return evt
		case tcell.KeyCtrlA, tcell.KeyCtrlE, tcell.KeyCtrlU, tcell.KeyCtrlK:
			// Allow common editing shortcuts
			return evt
		}
		// Block all other keys
		return nil
	})

	// Handle list events
	list.SetInputCapture(func(evt *tcell.EventKey) *tcell.EventKey {
		switch evt.Key() {
		case tcell.KeyEscape:
			dismissSkyUseDialog(app)
			return nil
		case tcell.KeyUp:
			// If at top of list, move focus back to input
			if list.GetCurrentItem() == 0 {
				app.SetFocus(input)
				return nil
			}
			// Otherwise allow up navigation in list
			return evt
		case tcell.KeyDown:
			// Allow down navigation in list
			return evt
		case tcell.KeyEnter:
			// Allow selection
			return evt
		case tcell.KeyTab:
			// Move focus to input
			app.SetFocus(input)
			return nil
		}
		// Block all other keys
		return nil
	})

	// Build layout
	flex.AddItem(instructions, 1, 0, false)
	flex.AddItem(input, 1, 0, true)

	if len(history.Commands) > 0 {
		// Add a title for recent commands
		recentTitle := tview.NewTextView()
		recentTitle.SetDynamicColors(true)
		recentTitle.SetTextAlign(tview.AlignLeft)
		recentTitle.SetBackgroundColor(app.Styles.BgColor())
		recentTitle.SetTextColor(app.Styles.FgColor())
		recentTitle.SetText("\n[yellow::b]Recent (↑/↓ to select):[-:-:-]")
		flex.AddItem(recentTitle, 2, 0, false)

		// Add list
		listHeight := len(history.Commands)
		if listHeight > 8 {
			listHeight = 8
		}
		flex.AddItem(list, listHeight, 0, false)
	}

	flex.AddItem(helpText, 1, 0, false)

	// Calculate dialog size
	_, _, appWidth, appHeight := app.Content.Pages.GetRect()
	dialogWidth := appWidth * 60 / 100 // 60% of screen width (was 80%)
	if dialogWidth < 80 {
		dialogWidth = 80
	}
	if dialogWidth > 120 {
		dialogWidth = 120
	}
	dialogHeight := 8 + len(history.Commands)
	if len(history.Commands) > 8 {
		dialogHeight = 16
	}
	if dialogHeight > appHeight-4 {
		dialogHeight = appHeight - 4
	}

	// Center the dialog
	centered := tview.NewFlex()
	centered.SetDirection(tview.FlexRow)
	centered.AddItem(nil, 0, 1, false)
	centered.AddItem(tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(flex, dialogWidth, 0, true).
		AddItem(nil, 0, 1, false), dialogHeight, 0, true)
	centered.AddItem(nil, 0, 1, false)

	// Wrap in a custom modal that will be recognized by IsTopDialog()
	modal := NewSkyUseModal(centered, input, list)

	// Show the dialog
	app.Content.Pages.AddPage(skyUseDialogKey, modal, true, true)
	app.SetFocus(input)
}

// SkyUseModal is a custom modal that wraps our sky use dialog
// It mimics ui.ModalList so it's recognized by IsTopDialog()
type SkyUseModal struct {
	*tview.Box
	content *tview.Flex
	input   *tview.InputField
	list    *tview.List
}

func NewSkyUseModal(content *tview.Flex, input *tview.InputField, list *tview.List) *SkyUseModal {
	return &SkyUseModal{
		Box:     tview.NewBox(),
		content: content,
		input:   input,
		list:    list,
	}
}

func (m *SkyUseModal) Draw(screen tcell.Screen) {
	// Get screen dimensions
	screenWidth, screenHeight := screen.Size()

	// Set the modal to use the full screen (the content Flex handles centering)
	m.SetRect(0, 0, screenWidth, screenHeight)

	// Set content rect to full screen as well
	m.content.SetRect(0, 0, screenWidth, screenHeight)

	// Draw the content
	m.content.Draw(screen)
}

func (m *SkyUseModal) Focus(delegate func(p tview.Primitive)) {
	delegate(m.input)
}

func (m *SkyUseModal) HasFocus() bool {
	return m.input.HasFocus() || m.list.HasFocus()
}

func (m *SkyUseModal) MouseHandler() func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
	return m.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(p tview.Primitive)) (consumed bool, capture tview.Primitive) {
		consumed, capture = m.content.MouseHandler()(action, event, setFocus)
		if !consumed && action == tview.MouseLeftClick && m.InRect(event.Position()) {
			setFocus(m)
			consumed = true
		}
		return
	})
}

func (m *SkyUseModal) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	return m.WrapInputHandler(func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
		if handler := m.content.InputHandler(); handler != nil {
			handler(event, setFocus)
		}
	})
}

func dismissSkyUseDialog(app *App) {
	app.Content.RemovePage(skyUseDialogKey)
}

func handleSkyUse(app *App, text string) {
	// Clean up the text - remove any newlines or carriage returns
	text = strings.ReplaceAll(text, "\n", "")
	text = strings.ReplaceAll(text, "\r", "")
	text = strings.TrimSpace(text)

	if text == "" {
		app.Flash().Warn("No command provided")
		return
	}

	slog.Info("handleSkyUse called", "input_text", text)

	// Parse the sky use command
	cluster, namespace, service, err := parseSkyUseCommand(text)
	if err != nil {
		slog.Error("Failed to parse sky use command", slogs.Error, err, "text", text)
		app.Flash().Err(err)
		return
	}

	slog.Info("Parsed sky use command",
		slogs.Cluster, cluster,
		slogs.Namespace, namespace,
		"service", service)

	// Show progress message
	app.Flash().Infof("Switching to cluster=%s, namespace=%s...", cluster, namespace)

	// Switch context and namespace
	go switchContextAndNamespace(app, cluster, namespace, service)
}

func switchContextAndNamespace(app *App, cluster, namespace, service string) {
	// Run kubectl commands to atomically set both context and namespace
	// This avoids the race condition where K9s tries to access default namespace
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	slog.Info("Starting context/namespace switch via kubectl",
		"cluster", cluster,
		"namespace", namespace,
		"service", service)

	// First, use kubectl to switch context
	slog.Info("Switching context via kubectl", "cluster", cluster)
	cmd1 := exec.CommandContext(ctx, "kubectl", "config", "use-context", cluster)
	output, err := cmd1.CombinedOutput()
	if err != nil {
		slog.Error("Failed to switch context via kubectl",
			slogs.Error, err,
			"output", string(output))
		app.QueueUpdateDraw(func() {
			app.Flash().Errf("Failed to switch context: %s", err)
		})
		return
	}

	// Then, immediately set the namespace for this context
	slog.Info("Setting namespace via kubectl", "namespace", namespace)
	cmd2 := exec.CommandContext(ctx, "kubectl", "config", "set-context", "--current", "--namespace="+namespace)
	output, err = cmd2.CombinedOutput()
	if err != nil {
		slog.Error("Failed to set namespace via kubectl",
			slogs.Error, err,
			"output", string(output))
		app.QueueUpdateDraw(func() {
			app.Flash().Errf("Failed to set namespace: %s", err)
		})
		return
	}
	slog.Info("Kubectl namespace set complete", "output", string(output))

	// Verify the namespace was set correctly
	cmd3 := exec.CommandContext(ctx, "kubectl", "config", "view", "--minify", "-o", "jsonpath={.contexts[0].context.namespace}")
	verifyOutput, err := cmd3.CombinedOutput()
	if err != nil {
		slog.Warn("Failed to verify namespace", slogs.Error, err)
	} else {
		actualNS := strings.TrimSpace(string(verifyOutput))
		slog.Info("Verified current context namespace", "expected", namespace, "actual", actualNS)
		if actualNS != namespace {
			slog.Error("Namespace mismatch after kubectl set", "expected", namespace, "actual", actualNS)
		}
	}

	// Add a small delay to ensure kubectl config is flushed to disk
	time.Sleep(100 * time.Millisecond)

	// Now update K9s internal state to match
	app.QueueUpdateDraw(func() {
		slog.Info("Reloading K9s state after kubectl changes")

		// Stop current view
		if app.Content.Top() != nil {
			app.Content.Top().Stop()
		}

		// IMPORTANT: Set the namespace in K9s config FIRST before switchContext
		// This ensures switchContext uses the correct namespace when reloading
		slog.Info("Setting active namespace in K9s config (BEFORE switchContext)", "namespace", namespace)
		if err := app.Config.SetActiveNamespace(namespace); err != nil {
			slog.Error("Failed to set active namespace in config", slogs.Error, err)
			app.Flash().Errf("Failed to set namespace: %s", err)
			return
		}

		// Verify it was set
		verifyNS := app.Config.ActiveNamespace()
		slog.Info("K9s config active namespace verified", "namespace", verifyNS, "expected", namespace)

		app.Config.K9s.ToggleContextSwitch(true)
		defer app.Config.K9s.ToggleContextSwitch(false)

		// CRITICAL: Use the client's SwitchContext to reload the kubeconfig
		// This should pick up the namespace we set via kubectl
		slog.Info("Switching context via client to reload kubeconfig")
		if err := app.factory.Client().SwitchContext(cluster); err != nil {
			slog.Error("Failed to switch context via client", slogs.Error, err)
			app.Flash().Errf("Failed to switch context: %s", err)
			return
		}

		// Verify factory namespace is correct after switch
		factoryNS := app.factory.Client().ActiveNamespace()
		slog.Info("Factory namespace after client switch", "namespace", factoryNS, "expected", namespace)

		// Force set factory namespace (always, to be safe)
		slog.Info("Force setting factory namespace", "namespace", namespace)
		if err := app.factory.SetActiveNS(namespace); err != nil {
			slog.Error("Failed to set factory namespace", slogs.Error, err)
			app.Flash().Errf("Failed to set namespace: %s", err)
			return
		}

		// Re-init factory with the correct namespace (terminate and restart)
		slog.Info("Re-initializing factory", "namespace", namespace)
		app.factory.Terminate()
		app.factory.Start(namespace)

		// Verify after factory restart
		factoryNS = app.factory.Client().ActiveNamespace()
		slog.Info("Factory namespace after restart", "namespace", factoryNS, "expected", namespace)

		// Reload the current view (use just the GVR, not the full view string)
		// The full view might contain the old namespace, so we extract just the resource type
		currentView := app.Config.ActiveView()
		slog.Info("Current view string", "view", currentView)

		// Extract just the GVR part (before any space/namespace)
		viewParts := strings.Fields(currentView)
		gvr := currentView
		if len(viewParts) > 0 {
			gvr = viewParts[0]
		}

		slog.Info("Reloading view", "gvr", gvr, "namespace", namespace)
		app.gotoResource(gvr, "", true, true)

		// Reset cluster model
		app.clusterModel.Reset(app.factory)

		// Final verification
		finalNS := app.factory.Client().ActiveNamespace()
		slog.Info("Final factory namespace", "namespace", finalNS, "expected", namespace)

		// Save config
		if err := app.Config.Save(true); err != nil {
			slog.Error("Failed to save config", slogs.Error, err)
		}

		// Call sky use command to complete the environment switch
		slog.Info("Calling sky use command", "cluster", cluster, "namespace", namespace, "service", service)
		go executeSkyUseCommand(cluster, namespace, service)

		slog.Info("Context and namespace switch completed successfully",
			"cluster", cluster,
			"namespace", namespace)
		app.Flash().Infof("Successfully switched to %s::%s", cluster, namespace)
	})
}

// executeSkyUseCommand runs the sky use command in the background
func executeSkyUseCommand(cluster, namespace, service string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sky", "use", cluster, namespace, service)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("Failed to execute sky use command",
			slogs.Error, err,
			"output", string(output),
			"cluster", cluster,
			"namespace", namespace,
			"service", service)
		return
	}

	slog.Info("Sky use command executed successfully",
		"output", string(output),
		"cluster", cluster,
		"namespace", namespace,
		"service", service)
}

// parseSkyUseCommand parses a sky use command string.
// Expected formats:
//   - "sky use cluster namespace service"
//   - "cluster namespace service"
func parseSkyUseCommand(text string) (cluster, namespace, service string, err error) {
	text = strings.TrimSpace(text)
	parts := strings.Fields(text)

	// Remove "sky" and "use" if present
	if len(parts) >= 2 && parts[0] == "sky" && parts[1] == "use" {
		parts = parts[2:]
	} else if len(parts) >= 1 && parts[0] == "sky" {
		parts = parts[1:]
	}

	// Need at least 3 parts: cluster, namespace, service
	if len(parts) < 3 {
		return "", "", "", fmt.Errorf("invalid sky use format. Expected: sky use <cluster> <namespace> <service>")
	}

	cluster = parts[0]
	namespace = parts[1]
	service = parts[2]

	return cluster, namespace, service, nil
}
