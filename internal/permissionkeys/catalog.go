// Package permissionkeys holds the platform's permission key catalog: the tool
// names a policy can name, with the action each starts at and whether an admin
// may move it.
//
// It lives in the bus's binary rather than only in a migration because the
// catalog has to exist on a fresh installation, and after the storage move a
// migration is no longer where it can come from. The list below is the current
// state of the SQL set (048 seeded it, 076 added the two read-only tools, and 058
// and 062 have each retired one).
//
// The catalog is also admin-editable — the portal edits a key's description and
// default action — so seeding only inserts what is missing. See
// store.SeedPermissionKeys.
package permissionkeys

import "gitee.com/kobeyoung81/nanoprotocol"

// Keys returns the catalog this binary ships.
//
// "read_only" is the strong claim: it makes the portal lock the key to its
// default action, and it makes SaveAgentType reject a policy that would move it
// (store.validateReadOnlyRules). It is for tools that cannot damage anything —
// which is why the read-only shell and the read-only delegation are in it, and
// bash/write/edit are not.
func Keys() []nanoprotocol.PermissionKeyDef {
	allow := nanoprotocol.PermissionAllow
	ask := nanoprotocol.PermissionAsk

	return []nanoprotocol.PermissionKeyDef{
		// Inspection: safe to run with nothing but the user's question behind it.
		{ID: "read", Description: "Read files", DefaultAction: allow, ReadOnly: true},
		{ID: "list", Description: "List directories", DefaultAction: allow, ReadOnly: true},
		{ID: "grep", Description: "Search file contents", DefaultAction: allow, ReadOnly: true},
		{ID: "glob", Description: "Find files", DefaultAction: allow, ReadOnly: true},
		{ID: "webfetch", Description: "Fetch a URL", DefaultAction: allow, ReadOnly: true},
		{ID: "load_skill", Description: "Load a skill", DefaultAction: allow, ReadOnly: true},
		{ID: "question", Description: "Ask the user", DefaultAction: allow, ReadOnly: true},
		{ID: "readonly_bash", Description: "Read-only shell: whitelisted inspection and verification commands only", DefaultAction: allow, ReadOnly: true},
		{ID: "readonly_task", Description: "Delegate to read-only subagents only", DefaultAction: allow, ReadOnly: true},

		// Everything that can change something asks first, unless a policy says
		// otherwise.
		{ID: "todowrite", Description: "Update the todo list", DefaultAction: allow},
		// The batch entry point amplifies nothing: every inner call is judged by
		// the caller's own rules exactly like a top-level call, which is why its
		// own action is allow while bash/write/edit above still ask.
		{ID: "parallel_execution", Description: "Run several tool calls concurrently, each inner call judged by the caller's own rules", DefaultAction: allow},
		{ID: "bash", Description: "Run shell commands", DefaultAction: ask},
		{ID: "write", Description: "Write files", DefaultAction: ask},
		{ID: "edit", Description: "Edit files", DefaultAction: ask},
		{ID: "download_media", Description: "Download channel media", DefaultAction: ask},
		{ID: "upload_media", Description: "Upload channel media", DefaultAction: ask},
		{ID: "manage_skill", Description: "Create/update user skills", DefaultAction: ask},
		{ID: "cron", Description: "Schedule jobs", DefaultAction: ask},
		{ID: "task", Description: "Spawn subagents", DefaultAction: ask},

		// One key covers every tool of every configured MCP server: they arrive
		// and change without this catalog knowing about them.
		{ID: "mcp__*", Description: "MCP tools (all servers)", DefaultAction: ask},
	}
}
