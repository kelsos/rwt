package cli

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/kelsos/rwt/internal/detect"
	"github.com/kelsos/rwt/internal/envfile"
	"github.com/kelsos/rwt/internal/git"
	"github.com/kelsos/rwt/internal/rotki"
	"github.com/spf13/cobra"
)

func lsCmd() *cobra.Command {
	var live bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the umbrella's worktrees",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wts, err := git.List(cmd.Context(), rotki.HostWorktreePath())
			if err != nil {
				return err
			}
			if live {
				return lsLive(wts)
			}
			return lsPlain(wts)
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "also show each instance's slot, dev port and running state")
	return cmd
}

func lsPlain(wts []git.Worktree) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKTREE\tBRANCH\tINSTANCE-CAPABLE?\tDIRTY?")
	for _, w := range wts {
		cap := "no"
		if detect.Capability(w.Path).Capable {
			cap = "yes"
		}
		dirty := "clean"
		if w.Dirty {
			dirty = "dirty"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", filepath.Base(w.Path), branchOrDetached(w.Branch), cap, dirty)
	}
	return tw.Flush()
}

// lsLive augments the listing with live dev:web state read from the app's port
// registry: each worktree's INSTANCE_NAME, its allocated dev port, and whether
// that port is currently accepting connections. Read-only — rwt never writes the
// registry.
func lsLive(wts []git.Worktree) error {
	slots, err := rotki.ReadSlots()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read port registry: %v\n", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKTREE\tBRANCH\tINSTANCE\tDEV-PORT\tRUNNING?")
	for _, w := range wts {
		instance, devPort, running := "-", "-", "-"
		if name, ok, _ := envfile.ReadInstanceName(w.Path); ok && name != "" {
			instance = name
			if slot, found := slots[rotki.SanitizeInstanceName(name)]; found {
				port := rotki.PortsForSlot(slot).Dev
				devPort = strconv.Itoa(port)
				running = yesNo(portOpen(port))
			} else {
				running = "no" // allocated lazily on first dev:web run
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", filepath.Base(w.Path), branchOrDetached(w.Branch), instance, devPort, running)
	}
	return tw.Flush()
}

// portOpen reports whether something is listening on 127.0.0.1:port. A short
// timeout keeps `ls --live` snappy even when many instances are down.
func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func branchOrDetached(b string) string {
	if b == "" {
		return "(detached)"
	}
	return b
}
