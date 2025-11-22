// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/k9s/internal/view/cmd"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	skyPipelinesTitle    = "Sky Pipelines"
	skyPipelinesTitleFmt = " [aqua::b]%s "
)

// SkyPipelines presents a pipeline executions viewer.
type SkyPipelines struct {
	*tview.TextView

	app        *App
	styles     *config.Styles
	ansiWriter io.Writer
}

// NewSkyPipelines returns a new sky pipelines viewer.
func NewSkyPipelines(app *App) *SkyPipelines {
	s := &SkyPipelines{
		TextView: tview.NewTextView(),
		app:      app,
		styles:   app.Styles,
	}
	s.SetDynamicColors(true)
	s.SetWrap(true)
	s.SetScrollable(true)
	s.SetRegions(true)

	return s
}

// Init initializes the component.
func (s *SkyPipelines) Init(ctx context.Context) error {
	s.SetBorder(true)
	s.SetBorderPadding(1, 1, 1, 1)
	s.SetBackgroundColor(s.styles.BgColor())
	s.resetTitle()
	s.bindKeys()
	s.app.Styles.AddListener(s)

	// Create ANSI writer with app styles
	fgColor := s.styles.Views().Log.FgColor.String()
	bgColor := s.styles.Views().Log.BgColor.String()
	s.ansiWriter = tview.ANSIWriter(s.TextView, fgColor, bgColor)

	// Show loading message immediately
	s.showLoading()

	// Fetch pipelines asynchronously so UI appears right away
	go s.fetchPipelinesAsync(ctx)

	return nil
}

func (s *SkyPipelines) showLoading() {
	s.Clear()
	fmt.Fprint(s.ansiWriter, "[aqua::b]Pipeline Executions[-:-:-]\n\n")
	fmt.Fprint(s.ansiWriter, "[yellow::b]Loading pipelines...\n\n")
	fmt.Fprint(s.ansiWriter, "[white::-]Executing: sky cm-pipeline list-executions\n")
	fmt.Fprint(s.ansiWriter, "[white::-]This may take a few seconds...\n")
}

// Name returns the component name.
func (s *SkyPipelines) Name() string {
	return "sky-pipelines"
}

// Start starts the component.
func (s *SkyPipelines) Start() {}

// Stop stops the component.
func (s *SkyPipelines) Stop() {}

// InCmdMode checks if prompt is active.
func (*SkyPipelines) InCmdMode() bool {
	return false
}

// SetCommand sets the current command (not used).
func (*SkyPipelines) SetCommand(*cmd.Interpreter) {}

// SetFilter sets the filter text (not used).
func (*SkyPipelines) SetFilter(string, bool) {}

// SetLabelSelector sets the label selector (not used).
func (*SkyPipelines) SetLabelSelector(labels.Selector, bool) {}

// Hints returns menu hints.
func (s *SkyPipelines) Hints() model.MenuHints {
	return model.MenuHints{
		{Mnemonic: "esc", Description: "Back", Visible: true},
	}
}

// ExtraHints returns additional hints.
func (s *SkyPipelines) ExtraHints() map[string]string {
	return nil
}

// StylesChanged notifies skin changed.
func (s *SkyPipelines) StylesChanged(st *config.Styles) {
	s.styles = st
	s.SetBackgroundColor(st.BgColor())
	s.SetTextColor(st.FgColor())
}

func (s *SkyPipelines) bindKeys() {
	s.SetInputCapture(func(evt *tcell.EventKey) *tcell.EventKey {
		switch evt.Key() {
		case tcell.KeyEscape, tcell.KeyEnter:
			s.app.PrevCmd(nil)
			return nil
		}
		return evt
	})
}

func (s *SkyPipelines) resetTitle() {
	s.SetTitle(fmt.Sprintf(skyPipelinesTitleFmt, skyPipelinesTitle))
}

func (s *SkyPipelines) fetchPipelinesAsync(ctx context.Context) {
	defer func(t time.Time) {
		slog.Debug("Sky pipelines fetch time", slogs.Elapsed, time.Since(t))
	}(time.Now())

	// Create a context with timeout
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Execute sky cm-pipeline list-executions
	cmd := exec.CommandContext(cmdCtx, "sky", "cm-pipeline", "list-executions")
	output, err := cmd.CombinedOutput()

	// Update UI on the main thread
	s.app.QueueUpdateDraw(func() {
		if err != nil {
			slog.Error("Failed to fetch sky pipelines",
				slogs.Error, err,
				"output", string(output))
			s.showError(err, output)
			return
		}

		s.showResults(output)
	})
}

func (s *SkyPipelines) showError(err error, output []byte) {
	s.Clear()
	fmt.Fprint(s.ansiWriter, "[aqua::b]Pipeline Executions[-:-:-]\n\n")
	fmt.Fprint(s.ansiWriter, "[red::b]Error fetching pipelines:\n\n")
	fmt.Fprintf(s.ansiWriter, "[white::-]%s\n\n", err.Error())
	if len(output) > 0 {
		fmt.Fprintf(s.ansiWriter, "[white::-]%s\n", string(output))
	}
}

func (s *SkyPipelines) showResults(output []byte) {
	s.Clear()

	// Add a header with tview formatting
	fmt.Fprint(s.ansiWriter, "[aqua::b]Pipeline Executions[-:-:-]\n\n")

	// Write the raw output - ANSI codes will be handled by ANSIWriter
	fmt.Fprint(s.ansiWriter, string(output))

	slog.Info("Sky pipelines fetched successfully", "outputLen", len(output))
}

// GetTable returns the underlying table (not used for TextView, but required for interface).
func (s *SkyPipelines) GetTable() *Table {
	return nil
}
