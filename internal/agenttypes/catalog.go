// Package agenttypes holds the built-in agent type definitions the platform
// ships: the two session modes and the internal workers.
//
// They used to arrive as SQL migrations (049 seeded the conversational and role
// types; 057, 059, 060, 063, 070 and 076 shaped them). They have to come from the
// binary for the same reason the permission key catalog does: on a fresh
// installation there is no migration to run against the store.
//
// ⚠️ Prompt text is deliberately absent. It ships in internal/promptbundle and is
// seeded by `nanobus prompts seed`, which records a baseline hash so an admin
// edit survives; a definition that carried its prompt here would fight that
// bookkeeping — and the two seeders would overwrite each other's work.
//
// What is here is the *structure*: how a mode runs (strategy, context, output,
// binding), how many rounds it may take, which tools it starts with, and the
// policy it starts under.
package agenttypes

import "gitee.com/kobeyoung81/nanoprotocol"

// CompleteBelt is a role whose capability list is a decision about *every*
// catalog entry, rather than a handful of opt-ins.
type CompleteBelt struct {
	TypeID string
	// BuiltinTools are the tools this role may use; anything else in the catalog
	// is off for it.
	BuiltinTools []string
	// McpEnabled is the same decision for MCP servers, which no belt lists by
	// name — they are configured at runtime.
	McpEnabled bool
}

// Allows reports this role's decision for one catalog entry.
func (b CompleteBelt) Allows(kind, ref string) bool {
	if kind != "builtin_tool" {
		return b.McpEnabled
	}
	for _, tool := range b.BuiltinTools {
		if tool == ref {
			return true
		}
	}
	return false
}

// CompleteBelts returns the restricted roles, with the decisions the deployed
// database holds.
//
// They are needed because "absent means the catalog default" cuts both ways: a
// role that lists only the tools it wants inherits everything it does not list,
// and the catalog default is on — which is how a read-only explorer ended up
// with the write path. Migrations 080 (builtin tools) and 081 (MCP servers)
// repaired that in SQL, but after the storage move there is no migration left to
// run, so the decisions live here and the store enforces them whenever it learns
// about a catalog entry (store.completeRestrictedBelts).
//
// The lists are verbatim the rows of the frozen source database: 7 of the
// catalog's 22 tools for explore, 8 for scout, 18 for general, none at all for
// role_title, and only general keeps MCP servers. (The frozen database holds 17
// rows for role_title because that is how large the catalog was when the rows
// were written; what matters — and what this reproduces — is that every row is
// off.)
func CompleteBelts() []CompleteBelt {
	return []CompleteBelt{
		{
			TypeID:       "role_explore",
			BuiltinTools: []string{"read", "grep", "glob", "list", "webfetch", "load_skill", "parallel_execution"},
		},
		{
			TypeID:       "role_scout",
			BuiltinTools: []string{"read", "grep", "glob", "list", "webfetch", "load_skill", "readonly_bash", "parallel_execution"},
		},
		{
			// The title generator is a one-shot utility, and the deployed database
			// says so with a row for every catalog tool and not one of them on. An
			// absent row would hand it the whole tool surface instead.
			TypeID: "role_title",
		},
		{
			TypeID: "role_general",
			BuiltinTools: []string{
				"read", "grep", "glob", "list", "webfetch", "load_skill", "bash", "edit", "write",
				"readonly_bash", "todowrite", "question", "append_note", "cron", "manage_skill",
				"download_media", "upload_media", "parallel_execution",
			},
			McpEnabled: true,
		},
	}
}

// Definitions returns the built-in types, with the capability baselines each
// starts with.
//
// A definition with no baselines inherits the platform default for every tool it
// can reach. The ones that do list tools are the restricted belts: a read-only
// exploration subagent must not be handed the shell, and a plan session must not
// be handed the write path.
func Definitions() []nanoprotocol.AgentTypeDef {
	react, oneshot := nanoprotocol.StrategyReact, nanoprotocol.StrategyOneshot
	windowed, recent, incremental := nanoprotocol.ContextWindowed, nanoprotocol.ContextRecent, nanoprotocol.ContextIncremental
	task, conversation := nanoprotocol.ContextTask, nanoprotocol.ContextConversation
	stream, caller, summary, sessionMeta := nanoprotocol.OutputStream, nanoprotocol.OutputCaller, nanoprotocol.OutputSummary, nanoprotocol.OutputSessionMeta
	shared, scratch, isolated := nanoprotocol.SessionBindingShared, nanoprotocol.SessionBindingScratch, nanoprotocol.SessionBindingIsolated
	allow, deny := nanoprotocol.PermissionAllow, nanoprotocol.PermissionDeny

	// The user-facing modes. Both are session types; the difference between them
	// is the tool belt, and plan's is a deny-all followed by the read-only tools
	// (rules are last-match-wins, so the order is the policy).
	build := nanoprotocol.AgentTypeDef{
		ID: "build", Name: "Build", Description: "Default conversational (build) agent",
		Strategy: react, Context: windowed, Output: stream, SessionBinding: shared,
		// Build owns the general file tools; the plan artefact tool belongs to plan
		// only, and this explicit deny is what keeps the two belts disjoint.
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "plan_write", Action: deny}},
		// A tool the binary has never announced arrives switched off, so every type
		// that should have one has to say so — the batch entry point included.
		Capabilities: []nanoprotocol.TypeCapabilityBinding{
			{Kind: "builtin_tool", Ref: "parallel_execution", DefaultEnabled: true},
		},
		Seeded: true,
	}
	plan := nanoprotocol.AgentTypeDef{
		ID: "plan", Name: "Plan", Description: "Read-only planning agent. Researches and proposes a plan without side effects.",
		MaxRounds: 30,
		Strategy:  react, Context: windowed, Output: stream, SessionBinding: shared,
		Permissions: []nanoprotocol.PermissionRule{
			{Pattern: "*", Action: deny},
			{Pattern: "read", Action: allow},
			{Pattern: "list", Action: allow},
			{Pattern: "grep", Action: allow},
			{Pattern: "glob", Action: allow},
			{Pattern: "webfetch", Action: allow},
			{Pattern: "load_skill", Action: allow},
			{Pattern: "question", Action: allow},
			{Pattern: "todowrite", Action: allow},
			// The plan artefact is plan-only, and the two read-only tools are what
			// let a restricted mode establish facts instead of only asking.
			{Pattern: "plan_write", Action: allow},
			{Pattern: "readonly_bash", Action: allow},
			{Pattern: "readonly_task", Action: allow},
			// Rules are last-match-wins and plan leads with a deny-all, so the batch
			// entry point needs its own allow — every inner call is still judged by
			// the rules above.
			{Pattern: "parallel_execution", Action: allow},
		},
		Capabilities: []nanoprotocol.TypeCapabilityBinding{
			{Kind: "builtin_tool", Ref: "readonly_bash", DefaultEnabled: true},
			{Kind: "builtin_tool", Ref: "readonly_task", DefaultEnabled: true},
			{Kind: "builtin_tool", Ref: "parallel_execution", DefaultEnabled: true},
			// The catalog default is on, and plan's belt is a deny-all followed
			// by read-only allowances. Naming the tools plan must not have is
			// what keeps its tool list as honest as its permissions: without
			// these, the mutating tools would be registered and then rejected,
			// and the model would spend a call learning that.
			{Kind: "builtin_tool", Ref: "bash", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "write", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "edit", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "append_note", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "manage_skill", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "cron", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "task", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "download_media", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "upload_media", DefaultEnabled: false},
			{Kind: "builtin_tool", Ref: "plan_execute", DefaultEnabled: false},
		},
		Seeded: true,
	}

	// The internal workers. Each of these is reachable only through its own
	// invocation role, never from the user-facing catalogue.
	title := nanoprotocol.AgentTypeDef{
		ID: "role_title", Role: "title", Name: "Title", Description: "Conversation title generator",
		Strategy: oneshot, Context: recent, Output: sessionMeta, SessionBinding: scratch,
		Settings: map[string]string{"meta_key": "_title"},
		Seeded:   true,
	}
	summarize := nanoprotocol.AgentTypeDef{
		ID: "role_summarize", Role: "summarize", Name: "Summarize", Description: "Session summarizer",
		Strategy: oneshot, Context: incremental, Output: summary, SessionBinding: scratch,
		Seeded: true,
	}
	// Note what is deliberately not here: role_background and role_subagent.
	// Migration 063 (2026-09-20) retired both — the background task type died
	// with the task tool's `tasks`/`background` parameters, and the generic
	// `subagent` blueprint was superseded by the three shapes below. They still
	// exist as documents in databases created before this catalog shipped, and
	// the seeder never deletes; a fresh store simply never learns about them.

	// Subagents: read/write, read-only, and web research. Their belts are listed
	// rather than inherited because they run unattended inside another agent's
	// work — a subagent that can do everything is a way around the caller's
	// policy, not a parallel to it.
	readWriteBelt := []nanoprotocol.TypeCapabilityBinding{
		{Kind: "builtin_tool", Ref: "read", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "write", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "edit", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "list", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "grep", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "glob", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "bash", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "webfetch", DefaultEnabled: true},
	}
	readOnlyBelt := []nanoprotocol.TypeCapabilityBinding{
		{Kind: "builtin_tool", Ref: "read", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "list", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "grep", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "glob", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "webfetch", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "parallel_execution", DefaultEnabled: true},
		// Spelled out rather than left to the catalog default: a read-only role
		// never delegates, and "absent means on" is how a belt stops meaning what
		// it says.
		{Kind: "builtin_tool", Ref: "readonly_task", DefaultEnabled: false},
	}
	scoutBelt := []nanoprotocol.TypeCapabilityBinding{
		{Kind: "builtin_tool", Ref: "read", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "list", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "grep", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "glob", DefaultEnabled: true},
		// research needs to run commands to inspect dependencies; its prompt has
		// always said "read-only", and this is that promise made real.
		{Kind: "builtin_tool", Ref: "readonly_bash", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "webfetch", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "parallel_execution", DefaultEnabled: true},
		{Kind: "builtin_tool", Ref: "readonly_task", DefaultEnabled: false},
	}

	subagent := func(id, role, name, description string, rounds int, belt []nanoprotocol.TypeCapabilityBinding) nanoprotocol.AgentTypeDef {
		return nanoprotocol.AgentTypeDef{
			ID: id, Role: role, Name: name, Description: description, MaxRounds: rounds,
			Strategy: react, Context: task, Output: caller, SessionBinding: isolated,
			Capabilities: belt,
			Seeded:       true,
		}
	}

	memoryFlush := nanoprotocol.AgentTypeDef{
		ID: "utility_memory_flush", Role: "memory_flush", Name: "Memory flush",
		Description: "Extracts durable memory before compaction.",
		MaxRounds:   1,
		Strategy:    oneshot, Context: conversation, Output: sessionMeta, SessionBinding: scratch,
		Settings: map[string]string{"meta_key": "_pending_memory_flush"},
		Seeded:   true,
	}

	return []nanoprotocol.AgentTypeDef{
		build,
		plan,
		title,
		summarize,
		// role_general states the batch row; the three shapes are the whole
		// subagent surface (a "subagent" blueprint would inherit every catalog
		// default, which is the opposite of what a delegated worker should get).
		subagent("role_general", "general", "Subagent: general", "Read/write subagent", 15,
			append(append([]nanoprotocol.TypeCapabilityBinding{}, readWriteBelt...),
				nanoprotocol.TypeCapabilityBinding{Kind: "builtin_tool", Ref: "parallel_execution", DefaultEnabled: true},
				// Stated out loud for the same reason as the read-only belts: a
				// general subagent must not delegate further.
				nanoprotocol.TypeCapabilityBinding{Kind: "builtin_tool", Ref: "readonly_task", DefaultEnabled: false})),
		subagent("role_explore", "explore", "Subagent: explore", "Read-only exploration", 12, readOnlyBelt),
		subagent("role_scout", "scout", "Subagent: scout", "Web research subagent", 12, scoutBelt),
		memoryFlush,
	}
}
