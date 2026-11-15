// Package cli is the command-line front end of SoundGit.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/DenisAmell/soundgit/core/internal/diff"
	"github.com/DenisAmell/soundgit/core/internal/repo"
)

// Version of the soundgit binary.
const Version = "0.1.0"

// Exit codes.
const (
	OK    = 0
	Fail  = 1
	Usage = 2
)

const usage = `soundgit — version control for DAW projects

Usage: soundgit [-C <folder>] <command> [arguments]

Commands:
  init [<project file>]        start tracking the project in this folder
  status [--json]              show what changed since the last version
  commit -m <message>          save the current state as a new version
  log [--json] [--safety]      list versions, newest first
  diff [--json] [--view] [<version> [<version>]]
                               compare versions, or a version with the
                               current state
  restore <version>            bring the project back to a version
  version                      print the soundgit version

A version is HEAD, HEAD~N, an id, or the first characters of an id.
`

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

// Run executes one command line and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	dir := "."
	for len(args) >= 2 && args[0] == "-C" {
		dir, args = args[1], args[2:]
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return Usage
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
	case "version", "--version":
		fmt.Fprintf(stdout, "soundgit %s\n", Version)
	case "init":
		err = cmdInit(dir, rest, stdout)
	case "status":
		err = cmdStatus(dir, rest, stdout)
	case "commit":
		err = cmdCommit(dir, rest, stdout, stderr)
	case "log":
		err = cmdLog(dir, rest, stdout)
	case "diff":
		err = cmdDiff(dir, rest, stdout)
	case "restore":
		err = cmdRestore(dir, rest, stdout, stderr)
	default:
		err = &usageError{fmt.Sprintf("unknown command %q", cmd)}
	}
	if err == nil {
		return OK
	}
	fmt.Fprintf(stderr, "soundgit: %v\n", err)
	var ue *usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(stderr, "Run 'soundgit help' for usage.")
		return Usage
	}
	return Fail
}

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string, minArgs, maxArgs int) error {
	if err := fs.Parse(args); err != nil {
		return &usageError{err.Error()}
	}
	if n := fs.NArg(); n < minArgs || n > maxArgs {
		return &usageError{fmt.Sprintf("%s: wrong number of arguments", fs.Name())}
	}
	return nil
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func cmdInit(dir string, args []string, out io.Writer) error {
	fs := flags("init")
	if err := parse(fs, args, 0, 1); err != nil {
		return err
	}
	r, err := repo.Init(dir, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Tracking %s in %s\n", r.Config.Project, r.Root)
	return nil
}

func cmdCommit(dir string, args []string, out, errw io.Writer) error {
	fs := flags("commit")
	msg := fs.String("m", "", "message")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	if strings.TrimSpace(*msg) == "" {
		return &usageError{"commit: a message is required (-m \"what changed\")"}
	}
	r, err := repo.Open(dir)
	if err != nil {
		return err
	}
	res, err := r.Commit(*msg)
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(errw, "warning: %s\n", w)
	}
	if res.NoChanges {
		fmt.Fprintln(out, "Nothing to commit: the project has not changed since the last version.")
		return nil
	}
	fmt.Fprintf(out, "Saved version %s\n", short(res.ID))
	return nil
}

func printAssets(out io.Writer, assets []repo.AssetChange, indent string) {
	for _, a := range assets {
		fmt.Fprintf(out, "%s%-9s %s\n", indent, a.Status+":", a.Path)
	}
}

func cmdStatus(dir string, args []string, out io.Writer) error {
	fs := flags("status")
	asJSON := fs.Bool("json", false, "")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	r, err := repo.Open(dir)
	if err != nil {
		return err
	}
	st, err := r.Status()
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, st)
	}
	if st.Head == nil {
		fmt.Fprintf(out, "On branch %s, no versions yet.\n", st.Branch)
	} else {
		fmt.Fprintf(out, "On branch %s, last version %s %q\n", st.Branch, short(st.Head.ID), st.Head.Message)
	}
	if st.ParseError != "" {
		fmt.Fprintf(out, "The project file cannot be read: %s\n", st.ParseError)
		return nil
	}
	if st.Head == nil {
		return nil
	}
	if st.Clean {
		fmt.Fprintln(out, "No changes since the last version.")
		return nil
	}
	if len(st.Changes) > 0 {
		fmt.Fprintln(out, "Changes since the last version:")
		diff.Text(out, st.Changes, "  ")
	}
	if len(st.Assets) > 0 {
		fmt.Fprintln(out, "Audio files:")
		printAssets(out, st.Assets, "  ")
	}
	return nil
}

func cmdLog(dir string, args []string, out io.Writer) error {
	fs := flags("log")
	asJSON := fs.Bool("json", false, "")
	safety := fs.Bool("safety", false, "")
	if err := parse(fs, args, 0, 0); err != nil {
		return err
	}
	r, err := repo.Open(dir)
	if err != nil {
		return err
	}
	list := r.Log
	if *safety {
		list = r.SafetyLog
	}
	versions, err := list()
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, versions)
	}
	if len(versions) == 0 {
		fmt.Fprintln(out, "No versions yet.")
	}
	for _, v := range versions {
		fmt.Fprintf(out, "%s  %s  %s  %s\n", short(v.ID), v.Time, v.Author, v.Message)
	}
	return nil
}

func cmdDiff(dir string, args []string, out io.Writer) error {
	fs := flags("diff")
	asJSON := fs.Bool("json", false, "")
	view := fs.Bool("view", false, "")
	if err := parse(fs, args, 0, 2); err != nil {
		return err
	}
	r, err := repo.Open(dir)
	if err != nil {
		return err
	}
	d, err := r.Diff(fs.Arg(0), fs.Arg(1), *view)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(out, d)
	}
	fmt.Fprintf(out, "From %s to %s\n", short(d.From), short(d.To))
	if len(d.Changes) == 0 && len(d.Assets) == 0 {
		fmt.Fprintln(out, "No differences.")
		return nil
	}
	diff.Text(out, d.Changes, "  ")
	if len(d.Assets) > 0 {
		fmt.Fprintln(out, "Audio files:")
		printAssets(out, d.Assets, "  ")
	}
	return nil
}

func cmdRestore(dir string, args []string, out, errw io.Writer) error {
	fs := flags("restore")
	if err := parse(fs, args, 1, 1); err != nil {
		return err
	}
	r, err := repo.Open(dir)
	if err != nil {
		return err
	}
	res, err := r.Restore(fs.Arg(0))
	if res != nil && res.Safety != "" {
		fmt.Fprintf(out, "Your uncommitted work was saved as safety version %s\n", short(res.Safety))
		fmt.Fprintf(out, "  (bring it back with: soundgit restore %s)\n", short(res.Safety))
	}
	if err != nil {
		return err
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(errw, "warning: %s\n", w)
	}
	fmt.Fprintf(out, "Restored version %s: %d files written, %d already matched.\n",
		short(res.ID), len(res.Written), res.Unchanged)
	fmt.Fprintln(out, "Reopen the project in your DAW. Commit to record this state as the newest version.")
	return nil
}
