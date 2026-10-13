package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/osean-man/pinner/internal/config"
	"github.com/osean-man/pinner/internal/synccrypto"
	"github.com/osean-man/pinner/internal/syncer"
	"github.com/spf13/cobra"
)

const (
	// menuSyncTimeout bounds how long opening the menu waits on the network.
	menuSyncTimeout = 1500 * time.Millisecond
	// writeSyncTimeout bounds the push after adding, updating or removing a pin.
	writeSyncTimeout = 3 * time.Second
	envServer        = "PINNER_SERVER"
	envSyncOff       = "PINNER_SYNC"
)

func loadSyncClient() (*syncer.Client, *syncer.Config, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := syncer.LoadConfig(dir)
	if err != nil || cfg == nil {
		return nil, nil, err
	}
	c, err := syncer.NewClient(*cfg)
	return c, cfg, err
}

// autoSync syncs in the background of a normal command when sync is set up. Failures only warn:
// pinner must keep working offline.
func autoSync(timeout time.Duration) {
	if os.Getenv(envSyncOff) == "off" {
		return
	}
	c, _, err := loadSyncClient()
	if err != nil {
		warn("sync disabled: %v", err)
		return
	}
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := syncer.Sync(ctx, store, c)
	reportRejected(res)
	if err != nil {
		warn("sync skipped (offline?): %v", shortErr(err))
	}
}

func shortErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timed out")
	}
	return err
}

func reportRejected(res syncer.Result) {
	if len(res.Rejected) == 0 {
		return
	}
	warn("WARNING: discarded %d synced record(s) that failed authentication. The sync server may be compromised or misconfigured. Nothing from them was saved or run.", len(res.Rejected))
	for _, e := range res.Rejected {
		warn("  %v", e)
	}
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync pins between devices with end-to-end encryption",
	Long: `Sync pins between your devices through a pinner-server. Pins are encrypted on your device
with a secret key that never leaves your devices; the server can't read or alter them.

  pinner sync init --server https://pins.example.com   # first device: creates your secret
  pinner sync join PINR-XXXX-... --server https://...  # other devices
  pinner sync                                          # sync now

Pins added with --local are never synced. Set PINNER_SYNC=off to disable automatic syncing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return syncNow()
	},
}

var serverFlag string

func syncNow() error {
	c, _, err := loadSyncClient()
	if err != nil {
		return err
	}
	if c == nil {
		return errors.New("sync is not set up; run \"pinner sync init\" or \"pinner sync join\"")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := syncer.Sync(ctx, store, c)
	reportRejected(res)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stderr, "Synced: pushed %d, received %d, updated %d.\n", res.Pushed, res.Received, res.Applied)
	return nil
}

func serverURL() (string, error) {
	s := serverFlag
	if s == "" {
		s = os.Getenv(envServer)
	}
	if s == "" {
		return "", fmt.Errorf("no server given: pass --server or set %s", envServer)
	}
	return syncer.ValidateServerURL(s)
}

// setUpSync saves the config, registers with the server and runs a first full sync.
func setUpSync(secret string) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	if existing, err := syncer.LoadConfig(dir); err != nil {
		return err
	} else if existing != nil {
		return errors.New("sync is already set up on this device; run \"pinner sync leave\" first")
	}
	server, err := serverURL()
	if err != nil {
		return err
	}
	cfg := syncer.Config{Server: server, Secret: secret}
	c, err := syncer.NewClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := c.Register(ctx); err != nil {
		return fmt.Errorf("could not register with %s: %w", server, err)
	}
	if err := store.ResetSyncState(); err != nil {
		return err
	}
	if err := syncer.SaveConfig(dir, cfg); err != nil {
		return err
	}
	res, err := syncer.Sync(ctx, store, c)
	reportRejected(res)
	if err != nil {
		return fmt.Errorf("sync set up, but the first sync failed (it will retry automatically): %w", err)
	}
	_, _ = fmt.Fprintf(os.Stderr, "Synced: pushed %d, received %d.\n", res.Pushed, res.Received)
	return nil
}

var syncInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Set up sync on your first device and create your secret key",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := synccrypto.GenerateSecret()
		if err != nil {
			return err
		}
		secret := synccrypto.EncodeSecret(raw)
		if err := setUpSync(secret); err != nil {
			return err
		}
		printSecret(secret)
		return nil
	},
}

func printSecret(secret string) {
	_, _ = fmt.Fprintln(os.Stderr, "\nYour sync secret (keep it private; anyone with it can read and change your pins):")
	fmt.Println(secret)
	_, _ = fmt.Fprintln(os.Stderr, "\nOn another device run:  pinner sync join <secret> --server <url>")
	_, _ = fmt.Fprintln(os.Stderr, "Show it again with:      pinner sync key")
	_, _ = fmt.Fprintln(os.Stderr, "If you lose it and all your devices, the synced copy cannot be recovered.")
}

var syncJoinCmd = &cobra.Command{
	Use:   "join <secret>",
	Short: "Join this device to your sync account using the secret from another device",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := synccrypto.DecodeSecret(args[0])
		if err != nil {
			return err
		}
		return setUpSync(synccrypto.EncodeSecret(raw))
	},
}

var syncKeyCmd = &cobra.Command{
	Use:   "key",
	Short: "Show this device's sync secret",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		_, cfg, err := loadSyncClient()
		if err != nil {
			return err
		}
		if cfg == nil {
			return errors.New("sync is not set up")
		}
		printSecret(cfg.Secret)
		return nil
	},
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show sync configuration and pending changes",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, cfg, err := loadSyncClient()
		if err != nil {
			return err
		}
		if cfg == nil {
			fmt.Println("Sync is not set up.")
			return nil
		}
		dirty, err := store.Dirty()
		if err != nil {
			return err
		}
		pending := 0
		for _, p := range dirty {
			if !p.Local || p.Synced {
				pending++
			}
		}
		pins, err := store.List("")
		if err != nil {
			return err
		}
		local := 0
		for _, p := range pins {
			if p.Local {
				local++
			}
		}
		device, _ := store.DeviceID()
		fmt.Printf("Server:          %s\n", cfg.Server)
		fmt.Printf("Account:         %.16s…\n", c.Keys().UserID)
		fmt.Printf("Device:          %s\n", device)
		fmt.Printf("Pins:            %d (%d local-only)\n", len(pins), local)
		fmt.Printf("Pending changes: %d\n", pending)
		return nil
	},
}

var purgeFlag bool

var syncLeaveCmd = &cobra.Command{
	Use:   "leave",
	Short: "Stop syncing on this device (pins stay on this device)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, cfg, err := loadSyncClient()
		if err != nil {
			return err
		}
		if cfg == nil {
			return errors.New("sync is not set up")
		}
		if purgeFlag {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.DeleteAccount(ctx); err != nil {
				return fmt.Errorf("could not delete server data: %w", err)
			}
			_, _ = fmt.Fprintln(os.Stderr, "Deleted all synced data from the server. Other devices keep their local copies.")
		}
		dir, err := config.Dir()
		if err != nil {
			return err
		}
		if err := syncer.RemoveConfig(dir); err != nil {
			return err
		}
		if err := store.ResetSyncState(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(os.Stderr, "Sync is off on this device. Your pins are still here.")
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{syncInitCmd, syncJoinCmd} {
		c.Flags().StringVar(&serverFlag, "server", "", "sync server URL (env "+envServer+")")
	}
	syncLeaveCmd.Flags().BoolVar(&purgeFlag, "purge", false, "also delete all of your synced data from the server")
	syncCmd.AddCommand(syncInitCmd, syncJoinCmd, syncKeyCmd, syncStatusCmd, syncLeaveCmd)
	rootCmd.AddCommand(syncCmd)
}
