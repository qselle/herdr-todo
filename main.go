package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

//go:embed VERSION
var versionFile string

func version() string { return strings.TrimSpace(versionFile) }

const usage = `Herdr Todo — plan work and keep a completion journal.

Usage: herdr-todo [--db PATH] [--project PATH] COMMAND [options]

Commands:
  ui                       Open the task interface (default)
  open                     Open the installed plugin as a Herdr popup
  path                     Print the database path
  add TITLE [--done] [--note TEXT]
  list [--all-projects] [--view active|today|history|all] [--search TEXT]
  done ID | doing ID | reopen ID
  edit ID [--title TEXT] [--note TEXT]
  journal [--id ID]         Show timestamped task events
  export                   Export every project and journal as JSON
  --version                Print version

CLI output is JSON. Global options go before the command.
`

// Keep flags usable after the positional title/ID, as in the original CLI.
func interspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	options, positional := []string{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			positional = append(positional, arg)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		f := fs.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown option %s", arg)
		}
		options = append(options, arg)
		boolFlag, isBool := f.Value.(interface{ IsBoolFlag() bool })
		if !hasValue && !(isBool && boolFlag.IsBoolFlag()) {
			i++
			if i == len(args) {
				return nil, fmt.Errorf("%s needs a value", arg)
			}
			options = append(options, args[i])
		}
	}
	return positional, fs.Parse(options)
}

func runCLI(args []string, stdout, stderr io.Writer) error {
	root := flag.NewFlagSet("herdr-todo", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	db := root.String("db", "", "database")
	project := root.String("project", "", "project directory")
	showVersion := root.Bool("version", false, "version")
	help := root.Bool("help", false, "help")
	root.BoolVar(help, "h", false, "help")
	if err := root.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, version())
		return err
	}
	if *help {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	command, rest := "ui", root.Args()
	if len(rest) > 0 {
		command, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cmdHelp := fs.Bool("help", false, "help")
	fs.BoolVar(cmdHelp, "h", false, "help")
	var done, all bool
	var title, note, view, search string
	var journalID int64
	wantArgs := 0
	switch command {
	case "ui", "open", "path", "export":
	case "add":
		fs.BoolVar(&done, "done", false, "record completed work")
		fs.StringVar(&note, "note", "", "note")
		wantArgs = 1
	case "list":
		fs.BoolVar(&all, "all-projects", false, "all projects")
		fs.StringVar(&view, "view", "active", "view")
		fs.StringVar(&search, "search", "", "search")
	case "done", "doing", "reopen":
		wantArgs = 1
	case "edit":
		fs.StringVar(&title, "title", "", "title")
		fs.StringVar(&note, "note", "", "note")
		wantArgs = 1
	case "journal":
		fs.Int64Var(&journalID, "id", 0, "task ID")
	default:
		return fmt.Errorf("unknown command %q; use --help", command)
	}
	pos, err := interspersed(fs, rest)
	if err != nil {
		return err
	}
	if *cmdHelp {
		_, err := io.WriteString(stdout, usage)
		return err
	}
	if len(pos) != wantArgs {
		return fmt.Errorf("%s expects %d argument(s); use --help", command, wantArgs)
	}
	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })
	var id int64
	if wantArgs == 1 && command != "add" {
		id, err = strconv.ParseInt(pos[0], 10, 64)
		if err != nil || id < 1 {
			return errors.New("Task ID must be a positive integer")
		}
	}
	if command == "journal" && visited["id"] && journalID < 1 {
		return errors.New("Task ID must be a positive integer")
	}
	if command == "open" {
		c, err := currentContext(*project)
		if err != nil {
			return err
		}
		out, err := openPopup(c, *db)
		if err != nil {
			return err
		}
		_, err = stdout.Write(out)
		return err
	}
	path, err := dbPath(*db)
	if err != nil {
		return err
	}
	if command == "path" {
		_, err := fmt.Fprintln(stdout, path)
		return err
	}
	if command == "ui" && (!term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd()))) {
		return errors.New("The task popup needs a terminal; use 'list' for JSON output")
	}
	var c Context
	if command == "ui" || command == "add" || command == "list" && !all {
		c, err = currentContext(*project)
		if err != nil {
			return err
		}
	}
	s, err := openStore(path)
	if err != nil {
		return err
	}
	defer s.Close()
	var result any
	switch command {
	case "ui":
		return runTUI(s, c)
	case "add":
		state := "planned"
		if done {
			state = "done"
		}
		result, err = s.Add(pos[0], c, state, note, nil)
	case "list":
		opt := ListOptions{View: view, Search: search}
		if !all {
			opt.Project = &c.Project
		}
		result, err = s.List(opt)
	case "done", "doing", "reopen":
		state := command
		if command == "reopen" {
			state = "planned"
		}
		result, err = s.SetState(id, state)
	case "edit":
		edit := Edit{}
		if visited["title"] {
			edit.Title = &title
		}
		if visited["note"] {
			edit.Note = &note
		}
		if edit.Title == nil && edit.Note == nil {
			return errors.New("Pass --title or --note")
		}
		result, err = s.Edit(id, edit)
	case "journal":
		var taskID *int64
		if visited["id"] {
			taskID = &journalID
		}
		result, err = s.Events(taskID)
	case "export":
		result, err = s.Export()
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(result)
}

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "herdr-todo:", err)
		os.Exit(1)
	}
}
