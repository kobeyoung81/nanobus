// Package main provides the nanobus CLI binary.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
	"gitee.com/kobeyoung81/nanobus/internal/config"
	buslog "gitee.com/kobeyoung81/nanobus/internal/log"
	"gitee.com/kobeyoung81/nanobus/internal/portalclient"
	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanobus/internal/server"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
	"github.com/spf13/cobra"
)

// Build-time variables set via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	setupLogger()

	var configPath string

	rootCmd := &cobra.Command{
		Use:   "nanobus",
		Short: "Nanobus - message routing and user registry service",
		Long: `Nanobus provides centralized message routing with a MongoDB-backed
user registry and per-user message queues. It exposes a WebSocket server
for gateway and agent connections.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadBusConfig(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if err := config.ValidateBusConfig(cfg); err != nil {
				return fmt.Errorf("invalid config: %w", err)
			}

			s, err := store.Open(cfg.Database.URI)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer s.Close()

			// The account domain is the auth service's: the store asks it for every
			// account read and write, and the provisioning queue is asked by
			// onboarding when it creates an account.
			s.SetAccountBackend(store.NewRemoteAccounts(authclient.New(cfg.Nanoauth.BaseURL, cfg.InternalToken)))
			s.SetContainerEnqueuer(provisionerclient.New(cfg.Provisioner.BaseURL, cfg.InternalToken))
			// Onboarding gives a new account an agent before asking for its
			// container: the rows are the portal's, and a container with no agent
			// answers nothing.
			s.SetAgentEnsurer(portalclient.New(cfg.Portal.BaseURL, cfg.InternalToken))

			srv := server.NewServer(s,
				server.WithNanoauthURL(cfg.Nanoauth.BaseURL),
				server.WithAuthURL(cfg.Nanoauth.BaseURL, cfg.InternalToken),
				server.WithAdminURL(cfg.Admin.BaseURL, cfg.InternalToken),
				// The owners answer their own domains: the catalogue is the admin
				// module's, the agent rows are the portal's, the fleet and the queue
				// are the provisioner's.
				server.WithAdminCatalogue(cfg.Admin.BaseURL, cfg.InternalToken),
				server.WithAgentSource(cfg.Portal.BaseURL, cfg.InternalToken),
				server.WithPortalURL(cfg.Portal.BaseURL, cfg.InternalToken),
				server.WithProvisionerURL(cfg.Provisioner.BaseURL, cfg.InternalToken),
				server.WithMaxContentBytes(cfg.Server.MaxContentBytes),
				server.WithInternalToken(cfg.InternalToken),
			)

			if admins, err := s.CountActiveAdmins(); err == nil && admins == 0 {
				slog.Warn("no administrator account yet",
					"hint", "the first account to register becomes the administrator (auth.firstUserIsAdmin); turn that off and assign the role by hand if registration is open to an untrusted network")
			}

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				sig := <-sigCh
				slog.Info("shutting down", "signal", sig)
				srv.Close()
			}()

			addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
			return srv.ListenAndServe(addr)
		},
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "", "path to bus config file")
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newUserCmd(&configPath))
	rootCmd.AddCommand(newMigrateCmd(&configPath))

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newUserCmd(configPath *string) *cobra.Command {
	userCmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users in the bus registry",
	}
	userCmd.AddCommand(newUserAddCmd(configPath))
	userCmd.AddCommand(newUserListCmd(configPath))
	userCmd.AddCommand(newUserLinkCmd(configPath))
	return userCmd
}

func newUserAddCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "add <user-id> [display-name]",
		Short: "Register a new user",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()
			displayName := ""
			if len(args) > 1 {
				displayName = args[1]
			}
			if err := s.RegisterUser(args[0], displayName); err != nil {
				return fmt.Errorf("register user: %w", err)
			}
			fmt.Printf("User %q registered.\n", args[0])
			return nil
		},
	}
}

func newUserListCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all registered users",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()
			users, err := s.ListUsers()
			if err != nil {
				return fmt.Errorf("list users: %w", err)
			}
			if len(users) == 0 {
				fmt.Println("No users registered.")
				return nil
			}
			for _, u := range users {
				fmt.Println(u)
			}
			return nil
		},
	}
}

func newUserLinkCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "link <user-id> <channel> <sender-id>",
		Short: "Link a channel identity to a user",
		Long: `Associate a platform identity with a registered user.

Examples:
  nanobus user link alice cli alice
  nanobus user link alice wecom WX1234567890
  nanobus user link alice feishu ou_abc123`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()
			userID, channel, senderID := args[0], args[1], args[2]
			if err := s.LinkChannel(userID, channel, senderID); err != nil {
				return fmt.Errorf("link channel: %w", err)
			}
			fmt.Printf("Linked %s/%s to user %q.\n", channel, senderID, userID)
			return nil
		},
	}
}

func openStore(configPath *string) (*store.Store, error) {
	cfg, err := config.LoadBusConfig(*configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return store.Open(cfg.Database.URI)
}

func newMigrateCmd(configPath *string) *cobra.Command {
	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Run database migrations",
	}
	migrateCmd.AddCommand(newMigrateUpCmd(configPath))
	migrateCmd.AddCommand(newMigrateStatusCmd(configPath))
	return migrateCmd
}

func newMigrateUpCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Apply all pending migrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			mongoManifests, err := migrationsmongo.Load()
			if err != nil {
				return fmt.Errorf("load mongo migrations: %w", err)
			}

			applied, err := s.Migrate(mongoManifests)
			if err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			if len(applied) == 0 {
				fmt.Println("Database is up to date.")
			} else {
				for _, name := range applied {
					fmt.Printf("  Applied: %s\n", name)
				}
				fmt.Printf("Applied %d migration(s).\n", len(applied))
			}
			return nil
		},
	}
}

func newMigrateStatusCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			mongoManifests, err := migrationsmongo.Load()
			if err != nil {
				return fmt.Errorf("load mongo migrations: %w", err)
			}

			mongoInfos, err := s.MigrationStatus(mongoManifests)
			if err != nil {
				return fmt.Errorf("migration status: %w", err)
			}
			printMigrationStatus(mongoInfos)
			return nil
		},
	}
}

// printMigrationStatus prints the migration manifests and whether each has run.
func printMigrationStatus(infos []store.MigrationInfo) {
	fmt.Println("mongodb:")
	if len(infos) == 0 {
		fmt.Println("  (none)")
		return
	}
	for _, info := range infos {
		if info.Applied {
			fmt.Printf("  ✓ %-45s  %s\n", info.Name, info.AppliedAt.Format("2006-01-02 15:04:05"))
		} else {
			fmt.Printf("  ✗ %s\n", info.Name)
		}
	}
}

// ci is a shorthand for creating a ConfigItem.
func setupLogger() {
	level := slog.LevelInfo
	if env := os.Getenv("LOG_LEVEL"); env != "" {
		switch strings.ToUpper(env) {
		case "VERBOSE":
			level = buslog.LevelVerbose
		case "DEBUG":
			level = slog.LevelDebug
		case "WARN":
			level = slog.LevelWarn
		case "ERROR":
			level = slog.LevelError
		}
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nanobus %s (commit: %s, built: %s)\n", version, commit, date)
		},
	}
}
