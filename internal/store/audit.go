package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// AuditEntry is a row in the audit_log table.
type AuditEntry struct {
	ID      int64  `json:"id"`
	ActorID string `json:"actor_id"`
	// ActorName is the account's display name, resolved when the entry is listed
	// rather than stored: the console shows a person instead of an id, and a name
	// that changes later must not be frozen into history.
	ActorName    string `json:"actor_name,omitempty"`
	ActorRole    string `json:"actor_role"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	// ResourceName is the display name behind ResourceID, resolved when the entry
	// is listed for the same reason ActorName is: the console shows an agent or a
	// person, not an id, and a name that changes later must not be frozen into
	// history. Empty for resources whose id reads as itself (containers, runners,
	// config keys) and for rows the lookup no longer finds.
	ResourceName string         `json:"resource_name,omitempty"`
	Detail       map[string]any `json:"detail,omitempty"`
	IP           string         `json:"ip"`
	CreatedAt    time.Time      `json:"created_at"`
}

// AlertRule is a row in the alert_rules table.
type AlertRule struct {
	ID        int64     `json:"id"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
	Metric    string    `json:"metric"`
	Threshold float64   `json:"threshold"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	auditLogCollection   = "audit_log"
	alertRulesCollection = "alert_rules"
)

// auditLog returns the audit trail collection, configured so a decoded nested
// value comes back as a map rather than the driver's default.
//
// The driver decodes an interface{} field into a bson.D — a list of pairs —
// which json.Marshal then renders as `[{"Key":…,"Value":…}]`. `detail` is passed
// straight through to the admin API, and the SQL's JSON column produced a plain
// object there, so DefaultDocumentM restores the shape the callers expect.
// Setting it on this collection (rather than on the client) keeps the choice
// visible where it matters and works for a Store built around any client.
func (s *Store) auditLog() *mongo.Collection {
	return s.db.Collection(auditLogCollection,
		options.Collection().SetBSONOptions(&options.BSONOptions{DefaultDocumentM: true}))
}

// storedAuditEntry is the document shape.
//
// The integer id stays the document's _id: it is what list ordering is
// tie-broken on, and what the admin API returns. `detail` is held as a document
// rather than the JSON *string* the SQL column held — the column was JSON too,
// and every reader (the bus and the portal alike) decodes it into a map, so
// there is nothing to gain from keeping the extra encoding.
type storedAuditEntry struct {
	ID           int64          `bson:"_id"`
	ActorID      string         `bson:"actor_id"`
	ActorRole    string         `bson:"actor_role"`
	Action       string         `bson:"action"`
	ResourceType string         `bson:"resource_type"`
	ResourceID   string         `bson:"resource_id"`
	Detail       map[string]any `bson:"detail,omitempty"`
	IP           string         `bson:"ip"`
	CreatedAt    time.Time      `bson:"created_at"`
}

func (d storedAuditEntry) entry() AuditEntry {
	return AuditEntry{
		ID: d.ID, ActorID: d.ActorID, ActorRole: d.ActorRole, Action: d.Action,
		ResourceType: d.ResourceType, ResourceID: d.ResourceID,
		Detail: d.Detail, IP: d.IP, CreatedAt: d.CreatedAt,
	}
}

// RecordAudit appends an entry to the audit trail.
func (s *Store) RecordAudit(entry AuditEntry) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The id comes from the sequence, not from the caller: the SQL column was
	// AUTO_INCREMENT and never read the struct's ID either.
	id, err := s.nextSequence(ctx, auditLogCollection)
	if err != nil {
		return err
	}

	doc := storedAuditEntry{
		ID: id, ActorID: entry.ActorID, ActorRole: entry.ActorRole,
		Action: entry.Action, ResourceType: entry.ResourceType,
		ResourceID: entry.ResourceID, Detail: entry.Detail, IP: entry.IP,
		CreatedAt: time.Now().UTC(),
	}
	// An unencodable detail surfaces here rather than from a separate json.Marshal.
	if _, err := s.auditLog().InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// AuditFilter narrows the audit trail. As on the conversation log, every
// non-empty field is its own condition and the console offers one control per
// condition; the conditions are ANDed.
type AuditFilter struct {
	Limit        int
	Action       string // exact, as picked from the trail's dropdown
	Actor        string // user id fragment, or a display name / email fragment
	ResourceType string // exact, as picked from the trail's dropdown
	Resource     string // resource id fragment, or the name behind it
}

// ListAudit returns recent audit entries, most recent first.
func (s *Store) ListAudit(f AuditFilter) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ctx, cancel := opCtx()
	defer cancel()

	// The names are resolved before the read: "the agent called ops-bot" has to
	// become a set of ids the filter can compare against, and the resource
	// fragment may name an agent, a user or a group.
	actorIDs, err := s.idsByNames(ctx, usersCollection, userSearchFields, f.Actor)
	if err != nil {
		return nil, err
	}
	resourceAgentIDs, err := s.idsByNames(ctx, agentsCollection, []string{"display_name"}, f.Resource)
	if err != nil {
		return nil, err
	}
	resourceUserIDs, err := s.idsByNames(ctx, usersCollection, userSearchFields, f.Resource)
	if err != nil {
		return nil, err
	}
	resourceGroupIDs, err := s.idsByNames(ctx, agentGroupsCollection, []string{"name"}, f.Resource)
	if err != nil {
		return nil, err
	}

	// _id breaks the tie: MySQL's TIMESTAMP had one-second resolution, so rows
	// written together compared equal and "most recent first" was whichever
	// order the storage happened to return. The sequence is monotonic, so it is
	// both a tie-break and a faithful reproduction of insertion order.
	cur, err := s.auditLog().Find(ctx,
		auditFilter(f, actorIDs, resourceAgentIDs, resourceUserIDs, resourceGroupIDs),
		options.Find().
			SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, fmt.Errorf("list audit: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	entries := []AuditEntry{}
	for cur.Next(ctx) {
		var doc storedAuditEntry
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode audit entry: %w", err)
		}
		entries = append(entries, doc.entry())
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}

	// The console shows an actor's name, not their id, so the names are resolved
	// here — once for the whole page rather than once per row, which is what the
	// SQL join this replaces cost.
	if err := s.fillActorNames(ctx, entries); err != nil {
		return nil, err
	}
	if err := s.fillResourceNames(ctx, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// auditFilter assembles the trail's conditions. The id sets are resolved by the
// caller, which keeps this a pure function the tests can assert on.
func auditFilter(f AuditFilter, actorIDs, resourceAgentIDs, resourceUserIDs, resourceGroupIDs []string) bson.D {
	clauses := bson.A{}
	if v := strings.TrimSpace(f.Action); v != "" {
		clauses = append(clauses, bson.D{{Key: "action", Value: v}})
	}
	if q := strings.TrimSpace(f.Actor); q != "" {
		clauses = append(clauses, matchesIDOrNames("actor_id", q, actorIDs))
	}
	if v := strings.TrimSpace(f.ResourceType); v != "" {
		clauses = append(clauses, bson.D{{Key: "resource_type", Value: v}})
	}
	if q := strings.TrimSpace(f.Resource); q != "" {
		// A resource name only means something once the type is known, so the
		// name branches are scoped by type; the id fragment stays unconditional.
		branches := bson.A{bson.D{{Key: "resource_id", Value: textPattern(q)}}}
		typed := []struct {
			resourceType string
			ids          []string
		}{
			{"agent", resourceAgentIDs},
			{"user", resourceUserIDs},
			{"agent_group", resourceGroupIDs},
		}
		for _, t := range typed {
			if len(t.ids) == 0 {
				continue
			}
			branches = append(branches, bson.D{{Key: "$and", Value: bson.A{
				bson.D{{Key: "resource_type", Value: t.resourceType}},
				bson.D{{Key: "resource_id", Value: bson.D{{Key: "$in", Value: t.ids}}}},
			}}})
		}
		clauses = append(clauses, bson.D{{Key: "$or", Value: branches}})
	}
	if len(clauses) == 0 {
		return bson.D{}
	}
	return bson.D{{Key: "$and", Value: clauses}}
}

// fillActorNames adds the display name of every distinct actor on a page.
func (s *Store) fillActorNames(ctx context.Context, entries []AuditEntry) error {
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ActorID)
	}
	names, err := s.namesByIDs(ctx, usersCollection, "display_name", distinctIDs(ids))
	if err != nil {
		return err
	}
	for i := range entries {
		entries[i].ActorName = names[entries[i].ActorID]
	}
	return nil
}

// fillResourceNames names the resources a page points at. Only the types whose id
// says nothing on its own are looked up — a container, a runner or a config key
// already reads as itself.
func (s *Store) fillResourceNames(ctx context.Context, entries []AuditEntry) error {
	agentIDs := make([]string, 0, len(entries))
	userIDs := make([]string, 0, len(entries))
	groupIDs := make([]string, 0, len(entries))
	for _, e := range entries {
		switch e.ResourceType {
		case "agent":
			agentIDs = append(agentIDs, e.ResourceID)
		case "user":
			userIDs = append(userIDs, e.ResourceID)
		case "agent_group":
			groupIDs = append(groupIDs, e.ResourceID)
		}
	}

	agentNames, err := s.namesByIDs(ctx, agentsCollection, "display_name", distinctIDs(agentIDs))
	if err != nil {
		return err
	}
	userNames, err := s.namesByIDs(ctx, usersCollection, "display_name", distinctIDs(userIDs))
	if err != nil {
		return err
	}
	groupNames, err := s.namesByIDs(ctx, agentGroupsCollection, "name", distinctIDs(groupIDs))
	if err != nil {
		return err
	}

	for i := range entries {
		switch entries[i].ResourceType {
		case "agent":
			entries[i].ResourceName = agentNames[entries[i].ResourceID]
		case "user":
			entries[i].ResourceName = userNames[entries[i].ResourceID]
		case "agent_group":
			entries[i].ResourceName = groupNames[entries[i].ResourceID]
		}
	}
	return nil
}

// storedAlertRule is the alert_rules document shape.
type storedAlertRule struct {
	ID        int64     `bson:"_id"`
	Severity  string    `bson:"severity"`
	Message   string    `bson:"message"`
	Metric    string    `bson:"metric"`
	Threshold float64   `bson:"threshold"`
	Enabled   bool      `bson:"enabled"`
	CreatedAt time.Time `bson:"created_at"`
	UpdatedAt time.Time `bson:"updated_at"`
}

func (d storedAlertRule) rule() AlertRule {
	return AlertRule{
		ID: d.ID, Severity: d.Severity, Message: d.Message,
		Metric: d.Metric, Threshold: d.Threshold, Enabled: d.Enabled,
		CreatedAt: d.CreatedAt,
	}
}

// ListAlertRules returns enabled alert rules.
func (s *Store) ListAlertRules() ([]AlertRule, error) {
	ctx, cancel := opCtx()
	defer cancel()

	// Sorted by _id, which is the order the SQL's ORDER BY id ASC asked for and
	// the order the admin UI lists them in.
	cur, err := s.db.Collection(alertRulesCollection).Find(ctx,
		bson.D{{Key: "enabled", Value: true}},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	rules := []AlertRule{}
	for cur.Next(ctx) {
		var doc storedAlertRule
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode alert rule: %w", err)
		}
		rules = append(rules, doc.rule())
	}
	return rules, cur.Err()
}

// UpsertAlertRule inserts or updates an alert rule, returning the id it holds
// afterwards — the console shows the rule it just created, and an insert is where
// that id is decided.
func (s *Store) UpsertAlertRule(rule AlertRule) (int64, error) {
	ctx, cancel := opCtx()
	defer cancel()
	coll := s.db.Collection(alertRulesCollection)
	now := time.Now().UTC()

	// A zero id means "new rule", exactly as it did with AUTO_INCREMENT.
	if rule.ID == 0 {
		id, err := s.nextSequence(ctx, alertRulesCollection)
		if err != nil {
			return 0, err
		}
		doc := storedAlertRule{
			ID: id, Severity: rule.Severity, Message: rule.Message, Metric: rule.Metric,
			Threshold: rule.Threshold, Enabled: rule.Enabled,
			CreatedAt: now, UpdatedAt: now,
		}
		if _, err := coll.InsertOne(ctx, doc); err != nil {
			return 0, fmt.Errorf("insert alert rule: %w", err)
		}
		return id, nil
	}

	// The SQL UPDATE matched zero rows for an unknown id and reported no error,
	// so neither does this.
	_, err := coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: rule.ID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "severity", Value: rule.Severity},
			{Key: "message", Value: rule.Message},
			{Key: "metric", Value: rule.Metric},
			{Key: "threshold", Value: rule.Threshold},
			{Key: "enabled", Value: rule.Enabled},
			// ON UPDATE CURRENT_TIMESTAMP, kept explicitly.
			{Key: "updated_at", Value: now},
		}}},
	)
	if err != nil {
		return 0, fmt.Errorf("update alert rule: %w", err)
	}
	return rule.ID, nil
}

// DeleteAlertRule removes an alert rule.
func (s *Store) DeleteAlertRule(id int64) error {
	ctx, cancel := opCtx()
	defer cancel()

	// Deleting an id that is not there is not an error, matching the SQL.
	if _, err := s.db.Collection(alertRulesCollection).DeleteOne(ctx, bson.D{{Key: "_id", Value: id}}); err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	return nil
}
