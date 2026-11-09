// Package main provides the nanobus CLI binary.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"gitee.com/kobeyoung81/nanobus/internal/agenttypes"
	"gitee.com/kobeyoung81/nanobus/internal/config"
	buslog "gitee.com/kobeyoung81/nanobus/internal/log"
	"gitee.com/kobeyoung81/nanobus/internal/permissionkeys"
	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
	"gitee.com/kobeyoung81/nanobus/internal/server"
	"gitee.com/kobeyoung81/nanobus/internal/skillbundle"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
	"gitee.com/kobeyoung81/nanoprotocol"
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

			srv := server.NewServer(s,
				server.WithNanoauthURL(cfg.Nanoauth.BaseURL),
				server.WithAdminURL(cfg.Admin.BaseURL),
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
	rootCmd.AddCommand(newConfigCmd(&configPath))
	rootCmd.AddCommand(newSkillsCmd(&configPath))
	rootCmd.AddCommand(newPromptsCmd(&configPath))
	rootCmd.AddCommand(newPermissionsCmd(&configPath))
	rootCmd.AddCommand(newAgentTypesCmd(&configPath))
	rootCmd.AddCommand(newMcpCmd(&configPath))
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

func newConfigCmd(configPath *string) *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage service configurations stored in the database",
	}
	// The defaults moved with the domain: `nanoadmin config seed` seeds them,
	// and writes go through the admin API. Only the read stays here.
	configCmd.AddCommand(newConfigGetCmd(configPath))
	return configCmd
}

func newConfigGetCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "get <component-type> [key]",
		Short: "Print configuration items for a component type, or a single key",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			if len(args) == 2 {
				item, err := s.GetConfigItem(args[0], args[1])
				if err != nil {
					return fmt.Errorf("get config item: %w", err)
				}
				fmt.Printf("%s = %s  [%s] %s\n", item.Key, item.Value, item.ValueType, item.Description)
				return nil
			}

			items, err := s.GetConfigItems(args[0])
			if err != nil {
				return fmt.Errorf("get config items: %w", err)
			}
			if len(items) == 0 {
				fmt.Printf("No config items for %q.\n", args[0])
				return nil
			}
			for _, item := range items {
				val := item.Value
				if item.ValueType == "secret" && val != "" {
					val = "****"
				}
				fmt.Printf("  %-40s = %-30s [%s]\n", item.Key, val, item.ValueType)
			}
			return nil
		},
	}
}

func newSkillsCmd(configPath *string) *cobra.Command {
	skillsCmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage the bundled skill catalog",
	}
	skillsCmd.AddCommand(newSkillsSeedCmd(configPath))
	return skillsCmd
}

func newSkillsSeedCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed bundled skill packages into the platform catalog",
		Long: `Upserts the read-only skills shipped with this binary into the platform
catalog (origin='bundled'). Re-running is safe: content is updated only when the
bundle version advances, and per-type baselines and per-user acks are preserved.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			bundled, err := skillbundle.Load()
			if err != nil {
				return fmt.Errorf("load skill bundle: %w", err)
			}
			defs := make([]nanoprotocol.SkillDef, 0, len(bundled))
			for _, b := range bundled {
				defs = append(defs, nanoprotocol.SkillDef{
					ID:               store.BundledSkillID(b.Name),
					Scope:            "platform",
					Name:             b.Name,
					Description:      b.Description,
					Files:            b.Files,
					Origin:           "bundled",
					Status:           "published",
					Version:          b.Version,
					PublishedVersion: b.Version,
					ContentHash:      b.ContentHash,
					CreatedBy:        "nanobus",
				})
			}
			inserted, updated, err := s.SeedPlatformSkills(defs)
			if err != nil {
				return fmt.Errorf("seed skills: %w", err)
			}
			fmt.Printf("Seeded %d bundled skill(s) (%d new, %d updated).\n", len(defs), inserted, updated)
			return nil
		},
	}
}

func newPromptsCmd(configPath *string) *cobra.Command {
	promptsCmd := &cobra.Command{
		Use:   "prompts",
		Short: "Manage the bundled agent-type system prompts",
	}
	promptsCmd.AddCommand(newPromptsSeedCmd(configPath))
	return promptsCmd
}

func newPromptsSeedCmd(configPath *string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "seed",
		Short: "Seed the bundled system prompts onto the agent types",
		Long: `Writes the system prompts shipped with this binary onto the seeded agent
types (agent_types.prompt). Prompt text is admin-editable, so an unmodified
platform seed is replaced while a prompt edited after seeding is left alone
unless --force is given. Re-running is safe.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			bundled, err := promptbundle.Load()
			if err != nil {
				return fmt.Errorf("load prompt bundle: %w", err)
			}
			updated, skipped, err := s.SeedPlatformPrompts(bundled, force)
			if err != nil {
				return fmt.Errorf("seed prompts: %w", err)
			}
			head, err := promptbundle.LoadSharedHead()
			if err != nil {
				return fmt.Errorf("load shared head prompt: %w", err)
			}
			headState := "absent"
			if head.Text != "" {
				headUpdated, err := s.SeedSessionHead(head, force)
				if err != nil {
					return fmt.Errorf("seed shared head prompt: %w", err)
				}
				headState = "left as-is"
				if headUpdated {
					headState = "updated"
				}
			}
			fmt.Printf("Seeded %d bundled prompt(s) (%d updated, %d left as-is); shared head prompt %s.\n",
				len(bundled), updated, skipped, headState)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "also overwrite prompts that were edited after seeding")
	return cmd
}

func newPermissionsCmd(configPath *string) *cobra.Command {
	permissionsCmd := &cobra.Command{
		Use:   "permissions",
		Short: "Manage the permission key catalog",
	}
	permissionsCmd.AddCommand(newPermissionsSeedCmd(configPath))
	return permissionsCmd
}

func newPermissionsSeedCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed the permission key catalog this binary ships",
		Long: `Inserts the permission keys the binary knows about, leaving existing ones
alone: descriptions and default actions are admin-editable in the portal, and
re-running this on every deploy must not undo those edits. Safe to re-run.

Retiring a key is a separate, deliberate act — an older binary must not be able
to delete a key a newer one ships — so nothing here removes anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			keys := permissionkeys.Keys()
			inserted, err := s.SeedPermissionKeys(keys)
			if err != nil {
				return fmt.Errorf("seed permission keys: %w", err)
			}
			fmt.Printf("Seeded %d permission key(s) (%d inserted, %d already present).\n",
				len(keys), inserted, len(keys)-inserted)
			return nil
		},
	}
}

func newAgentTypesCmd(configPath *string) *cobra.Command {
	agentTypesCmd := &cobra.Command{
		Use:   "agent-types",
		Short: "Manage the built-in agent type definitions",
	}
	agentTypesCmd.AddCommand(newAgentTypesSeedCmd(configPath))
	return agentTypesCmd
}

func newAgentTypesSeedCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed the built-in agent type definitions this binary ships",
		Long: `Inserts the platform's agent types (the build and plan session modes and
the internal workers) and the capability baselines they start with, leaving
anything that already exists exactly as it is: prompts, permissions and settings
are admin-editable, and a deploy must not undo those edits.

Prompt text is not part of this — it ships in the prompt bundle and is written by
"nanobus prompts seed". Safe to re-run.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			defs := agenttypes.Definitions()
			inserted, err := s.SeedAgentTypes(defs)
			if err != nil {
				return fmt.Errorf("seed agent types: %w", err)
			}
			fmt.Printf("Seeded %d agent type(s) (%d inserted, %d already present).\n",
				len(defs), inserted, len(defs)-inserted)
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

func newMcpCmd(configPath *string) *cobra.Command {
	mcpCmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage the platform MCP server catalog",
	}
	mcpCmd.AddCommand(newMcpSeedCmd(configPath))
	return mcpCmd
}

func newMcpSeedCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: "Seed the first-party MCP servers this binary ships",
		Long: `Inserts the platform's own MCP servers (today: nanoartifacts). An existing
row is left exactly as it is: URL, headers and defaults are admin-editable, and
re-running this on every deploy must not undo those edits. Safe to re-run.

The seeded row carries the shared internal token as its Authorization header,
read from NANOSEC_BUS_INTERNAL_TOKEN — the agent runtime sends catalog headers
verbatim, so the token has to live with the entry. Rotating that token therefore
needs this row updated; the seeder will not overwrite it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := strings.TrimSpace(os.Getenv("NANOSEC_BUS_INTERNAL_TOKEN"))
			if token == "" {
				return fmt.Errorf("NANOSEC_BUS_INTERNAL_TOKEN is required to seed the MCP row")
			}
			endpoint := strings.TrimSpace(os.Getenv("NANOARTIFACTS_MCP_URL"))
			if endpoint == "" {
				endpoint = "http://nanoartifacts:8080/mcp"
			}

			s, err := openStore(configPath)
			if err != nil {
				return err
			}
			defer s.Close()

			inserted, err := s.SeedMcpServer(store.McpServerRecord{
				Capability: store.Capability{
					ID:          "mcp:platform-nanoartifacts",
					Scope:       "platform",
					Name:        "artifacts",
					Description: "NanoArtifacts — platform work library (publish, list, share)",
					// Opt-in like every other tool this binary ships: a new
					// server must not silently widen what an agent can do.
					DefaultEnabled: false,
				},
				Transport: "http",
				URL:       endpoint,
				Headers:   map[string]string{"Authorization": "Bearer " + token},
			})
			if err != nil {
				return fmt.Errorf("seed mcp servers: %w", err)
			}
			if inserted {
				fmt.Printf("Seeded MCP server %q -> %s\n", "artifacts", endpoint)
			} else {
				fmt.Printf("MCP server %q already present; left unchanged.\n", "artifacts")
			}
			return nil
		},
	}
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
			backfilled, err := s.BackfillSessionTitles()
			if err != nil {
				return fmt.Errorf("backfill session titles: %w", err)
			}
			if len(applied) == 0 {
				fmt.Println("Database is up to date.")
			} else {
				for _, name := range applied {
					fmt.Printf("  Applied: %s\n", name)
				}
				fmt.Printf("Applied %d migration(s).\n", len(applied))
			}
			if backfilled > 0 {
				fmt.Printf("  Backfilled titles for %d session(s).\n", backfilled)
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
