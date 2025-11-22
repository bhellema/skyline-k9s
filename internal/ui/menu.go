// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package ui

import (
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/tview"
	runewidth "github.com/mattn/go-runewidth"
)

const (
	menuIndexFmt = " [key:-:b]<%d> [fg:-:fgstyle]%s "
	maxRows      = 8
)

var menuRX = regexp.MustCompile(`\d`)

// Menu presents menu options.
type Menu struct {
	*tview.Table

	styles        *config.Styles
	globalHintsFn func() model.MenuHints
}

// NewMenu returns a new menu.
func NewMenu(styles *config.Styles) *Menu {
	m := Menu{
		Table:  tview.NewTable(),
		styles: styles,
	}
	m.SetBackgroundColor(styles.BgColor())
	styles.AddListener(&m)

	return &m
}

// StylesChanged notifies skin changed.
func (m *Menu) StylesChanged(s *config.Styles) {
	m.styles = s
	m.SetBackgroundColor(s.BgColor())
	for row := range m.GetRowCount() {
		for col := range m.GetColumnCount() {
			if c := m.GetCell(row, col); c != nil {
				c.BackgroundColor = s.BgColor()
			}
		}
	}
}

// StackPushed notifies a component was added.
func (m *Menu) StackPushed(c model.Component) {
	m.HydrateMenu(c.Hints())
}

// StackPopped notifies a component was removed.
func (m *Menu) StackPopped(_, top model.Component) {
	if top != nil {
		m.HydrateMenu(top.Hints())
	} else {
		m.Clear()
	}
}

// StackTop notifies the top component.
func (m *Menu) StackTop(t model.Component) {
	m.HydrateMenu(t.Hints())
}

// SetGlobalHintsProvider registers a provider for global menu hints.
func (m *Menu) SetGlobalHintsProvider(f func() model.MenuHints) {
	m.globalHintsFn = f
}

// HydrateMenu populate menu ui from hints.
func (m *Menu) HydrateMenu(hh model.MenuHints) {
	m.Clear()

	if m.globalHintsFn != nil {
		if gh := m.globalHintsFn(); len(gh) > 0 {
			cp := make(model.MenuHints, len(hh))
			copy(cp, hh)
			hh = append(cp, gh...)
		}
	}

	// Sort all hints first
	sort.Sort(hh)

	// Separate hints into three categories
	skyHints, numHints, otherHints := m.categorizeHints(hh)

	// Build the table with clear column separation
	t := m.buildSimpleMenuTable(skyHints, numHints, otherHints)

	for row := range t {
		for col := range len(t[row]) {
			c := tview.NewTableCell(t[row][col])
			if t[row][col] == "" {
				c = tview.NewTableCell("")
			}
			c.SetBackgroundColor(m.styles.BgColor())
			m.SetCell(row, col, c)
		}
	}
}

// categorizeHints separates hints into three groups: Sky, Numeric, and Other.
func (*Menu) categorizeHints(hh model.MenuHints) (sky, num, other model.MenuHints) {
	for _, h := range hh {
		if !h.Visible {
			continue
		}

		if strings.HasPrefix(h.Description, "Sky") {
			sky = append(sky, h)
		} else if menuRX.MatchString(h.Mnemonic) {
			num = append(num, h)
		} else {
			other = append(other, h)
		}
	}

	slog.Info("Categorized hints",
		"skyCount", len(sky),
		"numCount", len(num),
		"otherCount", len(other))

	return sky, num, other
}

// buildSimpleMenuTable creates a simple table layout:
// Column 0: Sky commands
// Column 1: Numeric commands
// Column 2+: Other commands (alphabetically sorted)
func (m *Menu) buildSimpleMenuTable(skyHints, numHints, otherHints model.MenuHints) [][]string {
	// Calculate how many columns we need
	colCount := 0
	if len(skyHints) > 0 {
		colCount++ // Sky column
	}
	if len(numHints) > 0 {
		colCount++ // Numeric column
	}
	if len(otherHints) > 0 {
		// Other commands might need multiple columns
		otherCols := (len(otherHints) + maxRows - 1) / maxRows
		colCount += otherCols
	}

	slog.Info("Building simple menu table",
		"skyHints", len(skyHints),
		"numHints", len(numHints),
		"otherHints", len(otherHints),
		"totalColumns", colCount)

	// Create the table structure
	table := make([][]model.MenuHint, maxRows)
	for i := range table {
		table[i] = make([]model.MenuHint, colCount)
	}

	maxKeys := make([]int, colCount)
	currentCol := 0

	// Fill column 0 with Sky commands
	if len(skyHints) > 0 {
		for i, h := range skyHints {
			if i >= maxRows {
				break
			}
			table[i][currentCol] = h
			if len(h.Mnemonic) > maxKeys[currentCol] {
				maxKeys[currentCol] = len(h.Mnemonic)
			}
			slog.Debug("Placed Sky hint", "row", i, "col", currentCol, "mnemonic", h.Mnemonic, "desc", h.Description)
		}
		currentCol++
	}

	// Fill next column with Numeric commands
	if len(numHints) > 0 {
		for i, h := range numHints {
			if i >= maxRows {
				break
			}
			table[i][currentCol] = h
			if len(h.Mnemonic) > maxKeys[currentCol] {
				maxKeys[currentCol] = len(h.Mnemonic)
			}
			slog.Debug("Placed numeric hint", "row", i, "col", currentCol, "mnemonic", h.Mnemonic, "desc", h.Description)
		}
		currentCol++
	}

	// Fill remaining columns with Other commands
	if len(otherHints) > 0 {
		row := 0
		for _, h := range otherHints {
			table[row][currentCol] = h
			if len(h.Mnemonic) > maxKeys[currentCol] {
				maxKeys[currentCol] = len(h.Mnemonic)
			}
			slog.Debug("Placed other hint", "row", row, "col", currentCol, "mnemonic", h.Mnemonic, "desc", h.Description)

			row++
			if row >= maxRows {
				row = 0
				currentCol++
				if currentCol >= colCount {
					break
				}
			}
		}
	}

	// Convert table to strings
	out := make([][]string, maxRows)
	for r := range out {
		out[r] = make([]string, colCount)
		for c := range colCount {
			out[r][c] = m.formatMenu(table[r][c], maxKeys[c])
		}
	}

	return out
}

func (m *Menu) layout(table []model.MenuHints, mm []int, out [][]string) {
	for r := range table {
		for c := range table[r] {
			out[r][c] = m.formatMenu(table[r][c], mm[c])
		}
	}
}

func (m *Menu) formatMenu(h model.MenuHint, size int) string {
	if h.Mnemonic == "" || h.Description == "" {
		return ""
	}
	styles := m.styles.Frame()
	i, err := strconv.Atoi(h.Mnemonic)
	if err == nil {
		return formatNSMenu(i, h.Description, &styles)
	}

	return formatPlainMenu(h, size, &styles)
}

// ----------------------------------------------------------------------------
// Helpers...

func keyConv(s string) string {
	if s == "" || !strings.Contains(s, "alt") {
		return s
	}
	if runtime.GOOS != "darwin" {
		return s
	}

	return strings.Replace(s, "alt", "opt", 1)
}

// Truncate a string to the given l and suffix ellipsis if needed.
func Truncate(str string, width int) string {
	return runewidth.Truncate(str, width, string(tview.SemigraphicsHorizontalEllipsis))
}

func ToMnemonic(s string) string {
	if s == "" {
		return s
	}

	return "<" + keyConv(strings.ToLower(s)) + ">"
}

func formatNSMenu(i int, name string, styles *config.Frame) string {
	fmat := strings.Replace(menuIndexFmt, "[key", "["+styles.Menu.NumKeyColor.String(), 1)
	fmat = strings.ReplaceAll(fmat, ":bg:", ":"+styles.Title.BgColor.String()+":")
	fmat = strings.Replace(fmat, "[fg", "["+styles.Menu.FgColor.String(), 1)
	fmat = strings.Replace(fmat, "fgstyle]", styles.Menu.FgStyle.ToShortString()+"]", 1)

	return fmt.Sprintf(fmat, i, name)
}

func formatPlainMenu(h model.MenuHint, size int, styles *config.Frame) string {
	menuFmt := " [key:-:b]%-" + strconv.Itoa(size+2) + "s [fg:-:fgstyle]%s "
	fmat := strings.Replace(menuFmt, "[key", "["+styles.Menu.KeyColor.String(), 1)
	fmat = strings.Replace(fmat, "[fg", "["+styles.Menu.FgColor.String(), 1)
	fmat = strings.ReplaceAll(fmat, ":bg:", ":"+styles.Title.BgColor.String()+":")
	fmat = strings.Replace(fmat, "fgstyle]", styles.Menu.FgStyle.ToShortString()+"]", 1)

	return fmt.Sprintf(fmat, ToMnemonic(h.Mnemonic), h.Description)
}
