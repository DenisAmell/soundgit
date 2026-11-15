// Command audio-backuper-helper is the background process of the Audio
// Backuper plugin. The plugin starts it; it is not meant to be run by hand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/DenisAmell/soundgit/core/internal/backuper/helper"
	"github.com/DenisAmell/soundgit/core/internal/osutil"
)

func main() {
	detach := flag.Bool("detach", false, "start in the background and return at once")
	dataDir := flag.String("data-dir", "", "directory for state (default: per-user application data)")
	version := flag.Bool("version", false, "print the version")
	flag.Parse()

	if *version {
		fmt.Println(helper.Version)
		return
	}
	dir := *dataDir
	if dir == "" {
		var err error
		if dir, err = helper.DefaultDataDir(); err != nil {
			fmt.Fprintln(os.Stderr, "audio-backuper-helper:", err)
			os.Exit(1)
		}
	}

	if *detach {
		// Start a copy of ourselves that belongs to no DAW: it must outlive
		// the plugin's host to finish uploading.
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "audio-backuper-helper:", err)
			os.Exit(1)
		}
		cmd := exec.Command(self, "--data-dir", dir)
		osutil.Detach(cmd)
		if err := cmd.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "audio-backuper-helper:", err)
			os.Exit(1)
		}
		_ = cmd.Process.Release()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	opts := helper.Options{DataDir: dir}
	// Tests shorten the time the helper lingers with nobody connected.
	if s, err := strconv.Atoi(os.Getenv("AUDIO_BACKUPER_IDLE_EXIT_SECONDS")); err == nil && s > 0 {
		opts.IdleExit = time.Duration(s) * time.Second
	}
	err := helper.Run(ctx, opts)
	if errors.Is(err, helper.ErrAlreadyRunning) {
		return // the user already has a helper; that one serves everybody
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "audio-backuper-helper:", err)
		os.Exit(1)
	}
}
