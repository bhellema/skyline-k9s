// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package view

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal/slogs"
	"github.com/derailed/tcell/v2"
	"github.com/derailed/tview"
)

// ShowThreadDumpSaveDialog shows the save location dialog and executes thread dump for the selected pod
func ShowThreadDumpSaveDialog(app *App, podName string) {
	slog.Info("Showing thread dump save dialog", "pod", podName)

	const saveDialogKey = "thread-dump-save-dialog"

	// Create input field for folder path
	input := tview.NewInputField()
	input.SetLabel("Save to folder: ")
	input.SetFieldWidth(0)
	input.SetBackgroundColor(app.Styles.BgColor())
	input.SetLabelColor(app.Styles.FgColor())
	input.SetFieldTextColor(app.Styles.FgColor())
	input.SetFieldBackgroundColor(app.Styles.BgColor())

	// Default path: user's Downloads folder
	defaultPath := getDefaultDownloadFolder()
	input.SetText(defaultPath)

	// Create instructions
	instructions := tview.NewTextView()
	instructions.SetDynamicColors(true)
	instructions.SetTextAlign(tview.AlignLeft)
	instructions.SetBackgroundColor(app.Styles.BgColor())
	instructions.SetTextColor(app.Styles.FgColor())
	instructions.SetText(fmt.Sprintf("Capturing thread dump for: [yellow::b]%s[-:-:-]\n\nEnter the folder where you want to save the thread dump.\nPress Enter to confirm, Esc to cancel.", podName))

	// Handle input
	input.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			savePath := strings.TrimSpace(input.GetText())
			if savePath != "" {
				app.Content.Pages.RemovePage(saveDialogKey)
				executeThreadDump(app, podName, savePath)
			}
		} else if key == tcell.KeyEscape {
			app.Content.Pages.RemovePage(saveDialogKey)
		}
	})

	// Handle escape in input capture as well
	input.SetInputCapture(func(evt *tcell.EventKey) *tcell.EventKey {
		if evt.Key() == tcell.KeyEscape {
			app.Content.Pages.RemovePage(saveDialogKey)
			return nil
		}
		return evt
	})

	// Build layout
	flex := tview.NewFlex().SetDirection(tview.FlexRow)
	flex.SetBackgroundColor(app.Styles.BgColor())
	flex.SetBorder(true)
	flex.SetTitle(" Save Thread Dump ")
	flex.SetTitleColor(tcell.ColorAqua)
	flex.SetBorderColor(app.Styles.Frame().Border.FgColor.Color())

	flex.AddItem(instructions, 5, 0, false)
	flex.AddItem(input, 1, 0, true)

	// Calculate dialog size
	dialogWidth := 80
	dialogHeight := 9

	// Center the dialog
	centered := tview.NewFlex().SetDirection(tview.FlexRow)
	centered.AddItem(nil, 0, 1, false)
	centered.AddItem(tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(flex, dialogWidth, 0, true).
		AddItem(nil, 0, 1, false), dialogHeight, 0, true)
	centered.AddItem(nil, 0, 1, false)

	// Show the dialog
	app.Content.Pages.AddPage(saveDialogKey, centered, true, true)
	app.SetFocus(input)
}

// getDefaultDownloadFolder returns the user's Downloads folder
func getDefaultDownloadFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "Downloads")
}

// executeThreadDump executes the thread dump capture command and saves to specified folder
func executeThreadDump(app *App, podName, saveFolder string) {
	slog.Info("Executing thread dump", "pod", podName, "save_folder", saveFolder)
	app.Flash().Infof("Capturing thread dump for %s...", podName)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		// Update status: Executing command
		app.QueueUpdateDraw(func() {
			app.Flash().Infof("Executing thread dump command for %s...", podName)
		})

		cmd := exec.CommandContext(ctx, "sky", "thread-dump", "capture", podName, "20", "1")

		// Create pipes to capture stdout and stderr in real-time
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			slog.Error("Failed to create stdout pipe", slogs.Error, err)
			app.QueueUpdateDraw(func() {
				app.Flash().Errf("Failed to start thread dump: %s", err)
			})
			return
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			slog.Error("Failed to create stderr pipe", slogs.Error, err)
			app.QueueUpdateDraw(func() {
				app.Flash().Errf("Failed to start thread dump: %s", err)
			})
			return
		}

		// Start the command
		if err := cmd.Start(); err != nil {
			slog.Error("Failed to start thread dump command", slogs.Error, err)
			app.QueueUpdateDraw(func() {
				app.Flash().Errf("Failed to start thread dump: %s", err)
			})
			return
		}

		// Buffer to collect all output
		var outputBuffer strings.Builder
		var mu sync.Mutex

		// Channels for communication
		charChan := make(chan byte, 1000)
		doneChan := make(chan bool)

		// Read stdout character by character
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := stdout.Read(buf)
				if n > 0 {
					charChan <- buf[0]
				}
				if err != nil {
					break
				}
			}
		}()

		// Read stderr character by character
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := stderr.Read(buf)
				if n > 0 {
					charChan <- buf[0]
				}
				if err != nil {
					break
				}
			}
		}()

		// Process output character by character and update flash messages
		go func() {
			var currentLine strings.Builder
			var dotCount int
			var capturingDots bool
			var lastProgressUpdate time.Time

			for {
				select {
				case char := <-charChan:
					mu.Lock()
					outputBuffer.WriteByte(char)
					mu.Unlock()

					// Handle newlines
					if char == '\n' {
						line := currentLine.String()
						currentLine.Reset()

						// Reset dot tracking on new line
						if capturingDots && dotCount > 0 {
							// Show final message after dots complete
							finalLine := line
							app.QueueUpdateDraw(func() {
								app.Flash().Infof("%s", finalLine)
							})
						}
						capturingDots = false
						dotCount = 0

						// Update flash with interesting progress lines
						lineLower := strings.ToLower(line)
						if strings.Contains(lineLower, "connecting") {
							capturingDots = true // Start watching for dots after "Connecting"
							trimmedLine := strings.TrimSpace(line)
							app.QueueUpdateDraw(func() {
								app.Flash().Infof("%s", trimmedLine)
							})
						} else if strings.Contains(lineLower, "capturing") ||
							strings.Contains(lineLower, "threaddump") ||
							strings.Contains(lineLower, "downloading") ||
							strings.Contains(lineLower, "downloaded") {
							trimmedLine := strings.TrimSpace(line)
							app.QueueUpdateDraw(func() {
								app.Flash().Infof("%s", trimmedLine)
							})
						}
					} else {
						currentLine.WriteByte(char)

						// Track dots for progress indication
						if capturingDots && char == '.' {
							dotCount++
							now := time.Now()
							// Update flash every 2 dots or every 2 seconds to show progress
							if dotCount%2 == 0 || now.Sub(lastProgressUpdate) > 2*time.Second {
								lastProgressUpdate = now
								count := dotCount
								app.QueueUpdateDraw(func() {
									app.Flash().Infof("Capturing thread dump%s", strings.Repeat(".", count))
								})
							}
						}
					}

				case <-doneChan:
					return
				}
			}
		}()

		// Wait for command to complete
		cmdErr := cmd.Wait()
		close(doneChan)

		// Give output processing a moment to finish
		time.Sleep(100 * time.Millisecond)

		mu.Lock()
		outStr := strings.TrimSpace(outputBuffer.String())
		mu.Unlock()

		if cmdErr != nil {
			slog.Error("Thread dump command failed", slogs.Error, cmdErr, "output", outStr)
			app.QueueUpdateDraw(func() {
				app.Flash().Errf("Thread dump failed: %s", cmdErr)
			})
			return
		}

		slog.Info("Thread dump command completed", "output", outStr, "pod", podName)

		// Update status: Command completed, now moving file
		app.QueueUpdateDraw(func() {
			app.Flash().Infof("Thread dump captured, moving to %s...", saveFolder)
		})

		// Parse output to find the temporary location
		tempPath := parseThreadDumpLocation(outStr)
		if tempPath == "" {
			slog.Error("Could not parse thread dump location from output", "output", outStr)
			app.QueueUpdateDraw(func() {
				app.Flash().Err(fmt.Errorf("could not find thread dump file in command output"))
			})
			return
		}

		slog.Info("Found thread dump temp location", "temp_path", tempPath)

		// Extract filename from temp path
		filename := filepath.Base(tempPath)

		// Construct destination path (folder + filename)
		destPath := filepath.Join(expandPath(saveFolder), filename)

		// Move the file from temp location to user's folder
		if err := moveFile(tempPath, destPath); err != nil {
			slog.Error("Failed to move thread dump file", slogs.Error, err, "from", tempPath, "to", destPath)
			app.QueueUpdateDraw(func() {
				app.Flash().Errf("Failed to save thread dump: %s", err)
			})
			return
		}

		// Update status: Success
		slog.Info("Thread dump saved successfully", "path", destPath, "pod", podName)
		app.QueueUpdateDraw(func() {
			app.Flash().Infof("Thread dump saved to: %s", destPath)
		})
	}()
}

// parseThreadDumpLocation tries to extract the file path from sky command output
func parseThreadDumpLocation(output string) string {
	// The sky command outputs: "Downloaded thread dumps locally: /path/to/file.tar.gz"

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Look for "Downloaded thread dumps locally:" pattern
		if strings.Contains(line, "Downloaded thread dumps locally:") {
			if idx := strings.Index(line, ":"); idx != -1 {
				path := strings.TrimSpace(line[idx+1:])
				slog.Info("Found thread dump path", "path", path)
				return path
			}
		}

		// Also look for lines containing "saved", "written", "captured" followed by a path
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "saved") ||
			strings.Contains(lowerLine, "written") ||
			strings.Contains(lowerLine, "downloaded") {

			// Try to extract path after colon
			if idx := strings.Index(line, ":"); idx != -1 {
				path := strings.TrimSpace(line[idx+1:])
				if isValidThreadDumpPath(path) {
					slog.Info("Found thread dump path via keyword", "path", path)
					return path
				}
			}
		}

		// Try to match absolute paths ending with .tar.gz
		re := regexp.MustCompile(`([/~][^\s]+\.tar\.gz)`)
		if matches := re.FindStringSubmatch(line); len(matches) > 0 {
			path := matches[1]
			slog.Info("Found thread dump path via regex", "path", path)
			return path
		}
	}

	slog.Warn("No file path found in thread dump output")
	return ""
}

// isValidThreadDumpPath checks if a string looks like a valid thread dump file path
func isValidThreadDumpPath(path string) bool {
	if path == "" {
		return false
	}
	// Must start with / or ~ for absolute paths
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "~") {
		return false
	}
	// Should end with .tar.gz
	if !strings.HasSuffix(path, ".tar.gz") {
		return false
	}
	return true
}

// moveFile moves a file from source to destination
func moveFile(src, dst string) error {
	slog.Info("Moving thread dump file", "from", src, "to", dst)

	// Expand ~ in paths
	src = expandPath(src)
	dst = expandPath(dst)

	// Ensure destination directory exists
	dstDir := filepath.Dir(dst)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Try rename first (fastest if same filesystem)
	if err := os.Rename(src, dst); err == nil {
		slog.Info("File moved successfully using rename", "to", dst)
		return nil
	}

	// If rename fails, copy and delete
	slog.Debug("Rename failed, falling back to copy+delete", "src", src, "dst", dst)

	// Open source file
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	// Create destination file
	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	// Copy contents
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	// Sync to disk
	if err := dstFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync destination file: %w", err)
	}

	// Remove source file
	if err := os.Remove(src); err != nil {
		slog.Warn("Failed to remove source file after copy", slogs.Error, err, "src", src)
		// Don't return error since the file was copied successfully
	}

	slog.Info("File moved successfully using copy+delete", "to", dst)
	return nil
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
