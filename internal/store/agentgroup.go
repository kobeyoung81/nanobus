package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Agent grouping errors. The handlers map these onto HTTP status codes: a
// missing group or an unknown id in a reorder is 404, a taken name is 409, and
// an invalid name or an over-limit list is 400.
var (
	ErrAgentGroupNotFound     = errors.New("agent group not found")
	ErrAgentGroupNameTaken    = errors.New("an agent group with this name already exists")
	ErrAgentGroupLimit        = errors.New("too many agent groups")
	ErrAgentGroupNameInvalid  = errors.New("a group name is required (up to 40 characters)")
	ErrAgentGroupOrderInvalid = errors.New("the order must list every group exactly once")
)

const (
	agentGroupsCollection = "portal_agent_groups"
	// MaxAgentGroupsPerUser bounds the sidebar's group count. It is a guard
	// against runaway clients, not a product constraint.
	MaxAgentGroupsPerUser = 100
	// MaxAgentGroupNameRunes keeps a group header readable in the narrow
	// sidebar.
	MaxAgentGroupNameRunes = 40
)

// AgentGroup is a user-private grouping of agents. Groups are single-level and
// an agent belongs to at most one of them.
type AgentGroup struct {
	ID        string
	UserID    string
	Name      string
	Position  int
	CreatedAt time.Time
}

type storedAgentGroup struct {
	ID        string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	Name      string    `bson:"name"`
	Position  int       `bson:"position"`
	CreatedAt time.Time `bson:"created_at"`
}

func (d storedAgentGroup) group() AgentGroup {
	return AgentGroup{ID: d.ID, UserID: d.UserID, Name: d.Name, Position: d.Position, CreatedAt: d.CreatedAt}
}

// NormalizeAgentGroupName trims a group name and rejects empty or over-long
// ones. Uniqueness per user is enforced by the unique index, not by a read.
func NormalizeAgentGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxAgentGroupNameRunes {
		return "", ErrAgentGroupNameInvalid
	}
	return name, nil
}

// ListAgentGroups returns a user's groups in display order.
func (s *Store) ListAgentGroups(userID string) ([]AgentGroup, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(agentGroupsCollection).Find(ctx,
		bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "position", Value: 1}, {Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list agent groups: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	groups := []AgentGroup{}
	for cur.Next(ctx) {
		var doc storedAgentGroup
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan agent group: %w", err)
		}
		groups = append(groups, doc.group())
	}
	return groups, cur.Err()
}

// AgentGroupExists reports whether the group belongs to the user. An agent may
// only be placed in a group its owner created.
func (s *Store) AgentGroupExists(userID, groupID string) (bool, error) {
	if groupID == "" {
		return false, nil
	}
	ctx, cancel := opCtx()
	defer cancel()

	n, err := s.db.Collection(agentGroupsCollection).CountDocuments(ctx,
		bson.D{{Key: "_id", Value: groupID}, {Key: "user_id", Value: userID}})
	if err != nil {
		return false, fmt.Errorf("check agent group: %w", err)
	}
	return n > 0, nil
}
