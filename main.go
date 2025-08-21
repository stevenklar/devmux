package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type Proc struct {
	Name      string
	Dir       string
	Cmd       *exec.Cmd
	View      *tview.TextView
	Container *tview.Frame
	Follow    bool
	Argv      []string
}

type procSpec struct {
	Name   string   `json:"name"`
	Dir    string   `json:"dir,omitempty"`
	Cmd    string   `json:"cmd"`
	Args   []string `json:"args,omitempty"`
	Follow *bool    `json:"follow,omitempty"`
}

type procConfig struct {
	Processes []procSpec `json:"processes"`
	Borders   bool       `json:"borders"`
	Dividers  bool       `json:"dividers"`
}

type lockingWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockingWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// UI preferences (runtime toggles)
var (
	useBorders  = true
	useDividers = true
)

func loadConfig() (*procConfig, error) {
	wd, _ := os.Getwd()
	path := filepath.Join(wd, "procs.json")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var cfg procConfig
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func startProc(app *tview.Application, p *Proc, argv ...string) error {
	// Title and border are handled by the pane container (Frame)
	p.View.SetTitle("").SetBorder(false)
	p.View.SetScrollable(true)
	p.View.SetDynamicColors(true)
	p.View.SetWrap(false)
	p.View.SetChangedFunc(func() {
		app.QueueUpdateDraw(func() {
			if p.Follow {
				p.View.ScrollToEnd()
			}
		})
	})

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.Dir
	cmd.Env = append(os.Environ(),
		"FORCE_COLOR=1",
		"CLICOLOR=1",
		"CLICOLOR_FORCE=1",
		"TERM=xterm-256color",
	)

	if runtime.GOOS != "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	p.Cmd = cmd

	aw := tview.ANSIWriter(p.View)
	lw := &lockingWriter{w: aw}
	go func() { _, _ = io.Copy(lw, stdout) }()
	go func() { _, _ = io.Copy(lw, stderr) }()

	// Notify when it exits.
	go func() {
		err := cmd.Wait()
		fmt.Fprintf(p.View, "\n[%s] exited: %v\n", p.Name, err)
	}()

	return nil
}

// Build a vertical layout with one pane per process.
func buildMainArea(procs []*Proc) *tview.Flex {
	flex := tview.NewFlex().SetDirection(tview.FlexRow)
	for i, p := range procs {
		if useDividers && i > 0 {
			// Add a one-line spacer between panes (no border, draws nothing)
			flex.AddItem(tview.NewBox(), 1, 0, false)
		}
		// If a container frame exists, use it; else fall back to the view
		if p.Container != nil {
			flex.AddItem(p.Container, 0, 1, false)
		} else {
			flex.AddItem(p.View, 0, 1, false)
		}
	}
	return flex
}

// Render the header with dynamic status indicators.
func renderHeader(header *tview.TextView, app *tview.Application, procs []*Proc) {
	idx := focusedProcIndex(app, procs)
	focusedName := "-"
	followState := "-"
	if idx >= 0 {
		focusedName = procs[idx].Name
		if procs[idx].Follow {
			followState = "on"
		} else {
			followState = "off"
		}
	}
	header.SetText(fmt.Sprintf(
		"devmux — q: quit · TAB: focus · f: follow(%s) · r: reload · g/G: top/bottom · J/K: move pane · focus: %s",
		followState, focusedName,
	))
}

func makeProcsFromSpecs(specs []procSpec) ([]*Proc, [][]string) {
	procs := make([]*Proc, 0, len(specs))
	argvs := make([][]string, 0, len(specs))
	for _, s := range specs {
		follow := true
		if s.Follow != nil {
			follow = *s.Follow
		}
		tv := tview.NewTextView()
		frame := tview.NewFrame(tv)
		frame.SetBorder(useBorders)
		if useBorders {
			frame.SetTitle(s.Name)
		} else {
			frame.SetTitle("")
		}
		p := &Proc{Name: s.Name, Dir: s.Dir, View: tv, Container: frame, Follow: follow}
		procs = append(procs, p)
		args := make([]string, 0, 1+len(s.Args))
		args = append(args, s.Cmd)
		args = append(args, s.Args...)
		p.Argv = args
		argvs = append(argvs, args)
	}
	return procs, argvs
}

func focusedProcIndex(app *tview.Application, procs []*Proc) int {
	f := app.GetFocus()
	for i, p := range procs {
		if p.View == f {
			return i
		}
	}
	return -1
}

func moveProc(procs []*Proc, from, to int) []*Proc {
	if from < 0 || from >= len(procs) || to < 0 || to >= len(procs) || from == to {
		return procs
	}
	newOrder := make([]*Proc, 0, len(procs))
	for i, p := range procs {
		if i == from {
			continue
		}
		newOrder = append(newOrder, p)
	}
	if to >= len(newOrder) {
		newOrder = append(newOrder, procs[from])
		return newOrder
	}
	// insert at index `to`
	newOrder = append(newOrder[:to], append([]*Proc{procs[from]}, newOrder[to:]...)...)
	return newOrder
}

func killTree(p *Proc, sig syscall.Signal) {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = p.Cmd.Process.Kill()
		return
	}
	if pgid, err := syscall.Getpgid(p.Cmd.Process.Pid); err == nil {
		_ = syscall.Kill(-pgid, sig)
	} else {
		_ = p.Cmd.Process.Signal(sig)
	}
}

// reloadProc terminates the current process and restarts it using the
// original argv captured for the Proc.
func reloadProc(app *tview.Application, p *Proc) {
	if p == nil {
		return
	}
	if len(p.Argv) == 0 {
		fmt.Fprintf(p.View, "\n[%s] reload: no argv to start\n", p.Name)
		return
	}

	// Attempt graceful stop
	if p.Cmd != nil && p.Cmd.Process != nil {
		fmt.Fprintf(p.View, "\n[%s] reload: sending SIGTERM...\n", p.Name)
		killTree(p, syscall.SIGTERM)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if p.Cmd.ProcessState != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if p.Cmd.ProcessState == nil {
			fmt.Fprintf(p.View, "[%s] reload: forcing SIGKILL...\n", p.Name)
			killTree(p, syscall.SIGKILL)
			time.Sleep(150 * time.Millisecond)
		}
	}

	fmt.Fprintf(p.View, "[%s] reload: starting...\n", p.Name)
	_ = startProc(app, p, p.Argv...)
}

func main() {
	app := tview.NewApplication()

	header := tview.NewTextView().
		SetDynamicColors(true)

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "procs.json not found or invalid. Please create it and retry.")
		return
	}
	useBorders = cfg.Borders
	useDividers = cfg.Dividers

	procs, argvs := makeProcsFromSpecs(cfg.Processes)
	mainArea := buildMainArea(procs)
	renderHeader(header, app, procs)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(mainArea, 0, 1, true)

	app.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyTAB:
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				next := (idx + 1) % len(procs)
				app.SetFocus(procs[next].View)
			}
			renderHeader(header, app, procs)
			return nil
		case tcell.KeyCtrlL:
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				procs[idx].View.Clear()
			}
			return nil
		case tcell.KeyCtrlC:
			app.Stop()
			return nil
		}
		switch ev.Rune() {
		case 'q', 'Q':
			app.Stop()
			return nil
		case 'f':
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				procs[idx].Follow = !procs[idx].Follow
			}
			renderHeader(header, app, procs)
			return nil
		case 'r':
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				reloadProc(app, procs[idx])
			}
			return nil
		// case 'b': // toggle borders
		// 	useBorders = !useBorders
		// 	app.QueueUpdateDraw(func() {
		// 		for _, p := range procs {
		// 			if p.Container != nil {
		// 				p.Container.SetBorder(useBorders)
		// 				if useBorders {
		// 					p.Container.SetTitle(p.Name)
		// 				} else {
		// 					p.Container.SetTitle("")
		// 				}
		// 			} else {
		// 				p.View.SetBorder(useBorders)
		// 				if useBorders {
		// 					p.View.SetTitle(p.Name)
		// 				} else {
		// 					p.View.SetTitle("")
		// 				}
		// 			}
		// 		}
		// 	})
		// 	renderHeader(header, app, procs)
		// 	return nil
		// case 'd': // toggle dividers
		// 	useDividers = !useDividers
		// 	app.QueueUpdateDraw(func() {
		// 		mainArea = buildMainArea(procs)
		// 		root = tview.NewFlex().SetDirection(tview.FlexRow).
		// 			AddItem(header, 1, 0, false).
		// 			AddItem(mainArea, 0, 1, true)
		// 		_ = app.SetRoot(root, true)
		// 	})
		// 	renderHeader(header, app, procs)
		// 	return nil
		case 'g':
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				procs[idx].View.ScrollToBeginning()
			}
			return nil
		case 'G':
			idx := focusedProcIndex(app, procs)
			if idx >= 0 {
				procs[idx].View.ScrollToEnd()
			}
			return nil
		case 'J': // move pane down
			idx := focusedProcIndex(app, procs)
			if idx >= 0 && idx+1 < len(procs) {
				procs = moveProc(procs, idx, idx+1)
				mainArea = buildMainArea(procs)
				root = tview.NewFlex().SetDirection(tview.FlexRow).
					AddItem(header, 1, 0, false).
					AddItem(mainArea, 0, 1, true)
				_ = app.SetRoot(root, true)
				app.SetFocus(procs[idx+1].View)
				renderHeader(header, app, procs)
			}
			return nil
		case 'K': // move pane up
			idx := focusedProcIndex(app, procs)
			if idx > 0 {
				procs = moveProc(procs, idx, idx-1)
				mainArea = buildMainArea(procs)
				root = tview.NewFlex().SetDirection(tview.FlexRow).
					AddItem(header, 1, 0, false).
					AddItem(mainArea, 0, 1, true)
				_ = app.SetRoot(root, true)
				app.SetFocus(procs[idx-1].View)
				renderHeader(header, app, procs)
			}
			return nil
		}
		return ev
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		app.Stop()
	}()

	go func() {
		time.Sleep(120 * time.Millisecond)
		for i := range procs {
			_ = startProc(app, procs[i], argvs[i]...)
		}
	}()

	if err := app.SetRoot(root, true).EnableMouse(true).Run(); err != nil {
		log.Fatal(err)
	}

	for _, p := range procs {
		killTree(p, syscall.SIGTERM)
	}
	time.Sleep(2 * time.Second)
	for _, p := range procs {
		killTree(p, syscall.SIGKILL)
	}
}
