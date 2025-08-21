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
	Name   string
	Dir    string
	Cmd    *exec.Cmd
	View   *tview.TextView
	Follow bool
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

func loadProcSpecs() ([]procSpec, error) {
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
	return cfg.Processes, nil
}

func startProc(app *tview.Application, p *Proc, argv ...string) error {
	p.View.SetTitle(p.Name).SetBorder(true)
	p.View.SetScrollable(true)
	p.View.SetDynamicColors(true)
	p.View.SetWrap(false)
	p.View.SetChangedFunc(func() {
		if p.Follow {
			p.View.ScrollToEnd()
		}
		app.Draw()
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
	for _, p := range procs {
		flex.AddItem(p.View, 0, 1, false)
	}
	return flex
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
		p := &Proc{Name: s.Name, Dir: s.Dir, View: tv, Follow: follow}
		procs = append(procs, p)
		args := make([]string, 0, 1+len(s.Args))
		args = append(args, s.Cmd)
		args = append(args, s.Args...)
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

func main() {
	app := tview.NewApplication()

	header := tview.NewTextView().
		SetText("devmux — q: quit · TAB: focus · f: follow · g/G: top/bottom · J/K: move pane").
		SetDynamicColors(true)

	specs, err := loadProcSpecs()
	if err != nil || len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "procs.json not found or invalid. Please create it and retry.")
		return
	}
	procs, argvs := makeProcsFromSpecs(specs)
	mainArea := buildMainArea(procs)

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
			return nil
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
