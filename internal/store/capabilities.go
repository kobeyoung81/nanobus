package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Capability is a generic row shared by the per-kind catalog helpers.
type Capability struct {
	ID             string
	Scope          string
	OwnerUserID    string
	Name           string
	Description    string
	DefaultEnabled bool
	Locked         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// McpServerRecord is a row in mcp_servers.
type McpServerRecord struct {
	Capability
	Transport string
	URL       string
	Headers   map[string]string
}

// SkillRecord is a row in skills.
type SkillRecord struct {
	Capability
	Files            map[string]string
	Origin           string
	Version          int
	Status           string
	PublishedVersion int
	ContentHash      string
	CreatedBy        string
}

// errNotFound is returned when a catalog/id lookup misses.
var errCapNotFound = fmt.Errorf("capability not found")

// ======== builtin tools ========

const builtinToolsCollection = "admin_builtin_tools"

// storedBuiltinTool is a tool definition document.
//
// No migration manifest names this collection: every read is by _id (the list is
// ordered by it too), and MongoDB indexes _id with the collection.
type storedBuiltinTool struct {
	ID          string `bson:"_id"`
	Description string `bson:"description,omitempty"`
	// A JSON blob on both sides of the wire (the protocol carries it as
	// json.RawMessage) and never queried, so it stays the text the SQL column
	// held rather than becoming a document nothing reads field by field.
	Parameters     string    `bson:"parameters,omitempty"`
	DefaultEnabled bool      `bson:"default_enabled"`
	Locked         string    `bson:"locked,omitempty"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

func (s *Store) ListBuiltinTools() ([]nanoprotocol.BuiltinToolDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(builtinToolsCollection).Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list builtin tools: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []nanoprotocol.BuiltinToolDef{}
	for cur.Next(ctx) {
		var doc storedBuiltinTool
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode builtin tool: %w", err)
		}
		def := nanoprotocol.BuiltinToolDef{
			ID: doc.ID, Description: doc.Description,
			DefaultEnabled: doc.DefaultEnabled, Locked: doc.Locked,
		}
		// An unusable parameters blob is left empty, as it was when the SQL read
		// failed to unmarshal it.
		var raw json.RawMessage
		if json.Unmarshal([]byte(doc.Parameters), &raw) == nil {
			def.Parameters = raw
		}
		out = append(out, def)
	}
	return out, cur.Err()
}

// ======== mcp servers ========

const mcpServersCollection = "admin_mcp_servers"

// storedMcpServer is an MCP server document.
type storedMcpServer struct {
	ID    string `bson:"_id"`
	Scope string `bson:"scope"`
	// Written even when empty (the SQL stored NULL): the name-uniqueness check
	// compares it, and an absent field would not match an equality on "".
	OwnerUserID    string            `bson:"owner_user_id"`
	Name           string            `bson:"name"`
	Description    string            `bson:"description,omitempty"`
	Transport      string            `bson:"transport"`
	URL            string            `bson:"url,omitempty"`
	Headers        map[string]string `bson:"headers,omitempty"`
	DefaultEnabled bool              `bson:"default_enabled"`
	Locked         string            `bson:"locked,omitempty"`
	CreatedAt      time.Time         `bson:"created_at"`
	UpdatedAt      time.Time         `bson:"updated_at"`
}

func (d storedMcpServer) record() McpServerRecord {
	return McpServerRecord{
		Capability: Capability{
			ID: d.ID, Scope: d.Scope, OwnerUserID: d.OwnerUserID, Name: d.Name,
			Description: d.Description, DefaultEnabled: d.DefaultEnabled, Locked: d.Locked,
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		},
		Transport: d.Transport, URL: d.URL, Headers: orEmptyHeaders(d.Headers),
	}
}

// orEmptyHeaders keeps the shape the SQL scan produced: a JSON null or "{}" came
// back as an empty map, never as nil.
func orEmptyHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return map[string]string{}
	}
	return headers
}

// visibleCatalog is the visibility rule the per-kind readers share: platform
// rows for everyone, plus the caller's own.
func visibleCatalog(userID string) bson.D {
	return bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "scope", Value: "platform"}},
		bson.D{{Key: "scope", Value: "user"}, {Key: "owner_user_id", Value: userID}},
	}}}
}

func (s *Store) ListMcpServers(userID string) ([]McpServerRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(mcpServersCollection).Find(ctx, visibleCatalog(userID),
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []McpServerRecord{}
	for cur.Next(ctx) {
		var doc storedMcpServer
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode mcp server: %w", err)
		}
		out = append(out, doc.record())
	}
	return out, cur.Err()
}

func (s *Store) GetMcpServer(id string) (*McpServerRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedMcpServer
	err := s.db.Collection(mcpServersCollection).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errCapNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get mcp server: %w", err)
	}
	rec := doc.record()
	return &rec, nil
}
func (s *Store) ensureMcpNameFree(id, ownerUserID, name string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// Same soft check as the SQL: it counts the caller's own name collisions
	// before inserting, and two concurrent creates could still race — the
	// catalog has no unique key on (scope, owner, name) to lean on.
	count, err := s.db.Collection(mcpServersCollection).CountDocuments(ctx, bson.D{
		{Key: "scope", Value: "user"},
		{Key: "owner_user_id", Value: ownerUserID},
		{Key: "name", Value: name},
		{Key: "_id", Value: bson.D{{Key: "$ne", Value: id}}},
	})
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("an MCP server named %q already exists", name)
	}
	return nil
}

// ======== skills ========

const skillsCollection = "admin_skills"

// storedSkill is a skill document.
//
// The files map is stored as it is: its keys are file names ("SKILL.md") that a
// document holds happily, and the protocol hands the same map back.
type storedSkill struct {
	ID               string            `bson:"_id"`
	Scope            string            `bson:"scope"`
	OwnerUserID      string            `bson:"owner_user_id"`
	Name             string            `bson:"name"`
	Description      string            `bson:"description,omitempty"`
	Files            map[string]string `bson:"files"`
	DefaultEnabled   bool              `bson:"default_enabled"`
	Locked           string            `bson:"locked,omitempty"`
	Origin           string            `bson:"origin"`
	Version          int               `bson:"version"`
	Status           string            `bson:"status"`
	PublishedVersion int               `bson:"published_version"`
	ContentHash      string            `bson:"content_hash,omitempty"`
	CreatedBy        string            `bson:"created_by,omitempty"`
	CreatedAt        time.Time         `bson:"created_at"`
	UpdatedAt        time.Time         `bson:"updated_at"`
}

func (d storedSkill) record() SkillRecord {
	return SkillRecord{
		Capability: Capability{
			ID: d.ID, Scope: d.Scope, OwnerUserID: d.OwnerUserID, Name: d.Name,
			Description: d.Description, DefaultEnabled: d.DefaultEnabled, Locked: d.Locked,
			CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		},
		Files:            orEmptyFiles(d.Files),
		Origin:           d.Origin,
		Version:          d.Version,
		Status:           d.Status,
		PublishedVersion: d.PublishedVersion,
		ContentHash:      d.ContentHash,
		CreatedBy:        d.CreatedBy,
	}
}

// orEmptyFiles keeps the SQL scan's shape: a null or "{}" files column came back
// as an empty map, never nil.
func orEmptyFiles(files map[string]string) map[string]string {
	if files == nil {
		return map[string]string{}
	}
	return files
}

func (s *Store) ListSkills(userID string) ([]SkillRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(skillsCollection).Find(ctx, visibleCatalog(userID),
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []SkillRecord{}
	for cur.Next(ctx) {
		var doc storedSkill
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode skill: %w", err)
		}
		out = append(out, doc.record())
	}
	return out, cur.Err()
}

func (s *Store) GetSkill(id string) (*SkillRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedSkill
	err := s.db.Collection(skillsCollection).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errCapNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill: %w", err)
	}
	rec := doc.record()
	return &rec, nil
}

// ======== per-mode attachments ========
// DefaultModeTypeID returns the agent's default session type (or its primary
// type, or the platform default).
func (s *Store) DefaultModeTypeID(agentID string) string {
	if id := s.DefaultSessionAgent(agentID); id != "" {
		return id
	}
	// Via the agent accessor, not a raw read of the collection: the agents
	// document has exactly one reader, and it is agent.go.
	primary := ""
	if agent, _ := s.GetAgent(agentID); agent != nil {
		primary = agent.TypeID
	}
	if primary != "" {
		return primary
	}
	return DefaultAgentTypeID
}

// ResolveCapabilities resolves the catalog for an agent's DEFAULT mode.
func (s *Store) ResolveCapabilities(agentID string) (*nanoprotocol.CapabilitiesResponse, error) {
	return s.ResolveModeCapabilities(agentID, s.DefaultModeTypeID(agentID))
}

// effectivePermissions composes a definition's effective permission rules in
// last-match-wins order: type baseline, then per-capability rules, then the
// user's per-(agent, mode) override last so the override always wins. at must
// still carry its raw (un-merged) baseline, i.e. be passed before mergeOverride
// folds the override in.
func effectivePermissions(at *nanoprotocol.AgentTypeDef, ov *nanoprotocol.AgentTypeOverride) []nanoprotocol.PermissionRule {
	if at == nil {
		return nil
	}
	rules := append([]nanoprotocol.PermissionRule{}, at.Permissions...)
	for _, c := range at.Capabilities {
		rules = append(rules, c.Permissions...)
	}
	if ov != nil {
		rules = append(rules, ov.Permissions...)
	}
	return rules
}

// ResolveModeCapabilities returns the full catalog snapshot for one (agent,
// mode): builtin tools + MCP servers + skills visible to the agent's user, plus
// that mode's resolved attachments/overrides. MCP/skills are connected and
// skill files materialized per agent, so they follow the default mode;
// per-def builtin tool enablement is carried by ResolveAgentDefinitions.
func (s *Store) ResolveModeCapabilities(agentID, typeID string) (*nanoprotocol.CapabilitiesResponse, error) {
	agent, err := s.GetAgent(agentID)
	if err != nil {
		return nil, fmt.Errorf("resolve agent user: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	userID := agent.UserID

	builtin, err := s.ListBuiltinTools()
	if err != nil {
		return nil, err
	}
	mcp, err := s.ListMcpServers(userID)
	if err != nil {
		return nil, err
	}
	skills, err := s.ListSkills(userID)
	if err != nil {
		return nil, err
	}
	models, err := s.ListVisibleModels(userID)
	if err != nil {
		return nil, err
	}

	if typeID == "" {
		typeID = s.DefaultModeTypeID(agentID)
	}
	at, err := s.GetAgentType(typeID)
	if err != nil {
		return nil, err
	}
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return nil, err
	}
	var permRules []nanoprotocol.PermissionRule
	if at != nil {
		// Compose before mergeOverride folds the override into at.Permissions,
		// so the override is appended last (last-match-wins) rather than being
		// defeated by a per-capability rule.
		permRules = effectivePermissions(at, ov)
		mergeOverride(at, ov)
	}

	resp := &nanoprotocol.CapabilitiesResponse{
		BuiltinTools: builtin,
		McpServers:   nil,
		Skills:       nil,
		Attachments:  nil,
		Models:       models,
		AgentType:    at,
		Permissions:  permRules,
	}
	if ov != nil {
		resp.AgentPermissions = ov.Permissions
		resp.Attachments = append(resp.Attachments, ov.Capabilities...)
	}
	for _, m := range mcp {
		resp.McpServers = append(resp.McpServers, nanoprotocol.McpServerDef{
			ID:             m.ID,
			Scope:          m.Scope,
			OwnerUserID:    m.OwnerUserID,
			Name:           m.Name,
			Description:    m.Description,
			Transport:      m.Transport,
			URL:            m.URL,
			Headers:        m.Headers,
			DefaultEnabled: m.DefaultEnabled,
			Locked:         m.Locked,
		})
	}
	acks, _ := s.skillAckMap(userID)
	for _, sk := range skills {
		def := nanoprotocol.SkillDef{
			ID:               sk.ID,
			Scope:            sk.Scope,
			OwnerUserID:      sk.OwnerUserID,
			Name:             sk.Name,
			Description:      sk.Description,
			Files:            sk.Files,
			DefaultEnabled:   sk.DefaultEnabled,
			Locked:           sk.Locked,
			Origin:           sk.Origin,
			Version:          sk.Version,
			Status:           sk.Status,
			PublishedVersion: sk.PublishedVersion,
			ContentHash:      sk.ContentHash,
			CreatedBy:        sk.CreatedBy,
		}
		if sk.Scope == "platform" {
			acked := acks[sk.ID]
			def.AckedVersion = acked
			if acked > 0 && sk.Status == "published" && sk.PublishedVersion > acked {
				def.UpdateAvailable = true
			}
		}
		resp.Skills = append(resp.Skills, def)
	}
	return resp, nil
}

// AgentOwner returns the user ID that owns the given agent.
func (s *Store) AgentOwner(agentID string) (string, error) {
	agent, err := s.GetAgent(agentID)
	if err != nil {
		return "", fmt.Errorf("resolve agent owner: %w", err)
	}
	if agent == nil {
		return "", errCapNotFound
	}
	return agent.UserID, nil
}

// CapabilityVisible reports whether a catalog item of the given kind is visible
// to (and resolvable by) the user: global scope, or owned by the user.
func (s *Store) CapabilityVisible(kind, ref, userID string) (bool, error) {
	switch kind {
	case "builtin_tool":
		ctx, cancel := opCtx()
		defer cancel()
		count, err := s.db.Collection(builtinToolsCollection).CountDocuments(ctx, bson.D{{Key: "_id", Value: ref}})
		if err != nil {
			return false, err
		}
		return count > 0, nil
	case "mcp":
		rec, err := s.GetMcpServer(ref)
		if err != nil || rec == nil {
			return false, nil
		}
		if rec.Scope == "user" {
			return rec.OwnerUserID == userID, nil
		}
		return true, nil
	case "skill":
		rec, err := s.GetSkill(ref)
		if err != nil || rec == nil {
			return false, nil
		}
		if rec.Scope == "user" {
			return rec.OwnerUserID == userID, nil
		}
		return true, nil
	}
	return false, nil
}

func (c McpServerRecord) Id() string     { return c.ID }
func (c McpServerRecord) UserID() string { return c.OwnerUserID }

func (c SkillRecord) Id() string     { return c.ID }
func (c SkillRecord) UserID() string { return c.OwnerUserID }
