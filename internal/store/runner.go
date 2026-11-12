package store

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// runnersCollection belongs to the provisioner now: it registers and heartbeats
// its own row. This service keeps the console's read-only fleet view.
const runnersCollection = "provisioner_runners"

// storedRunner is the document shape the reads decode into.
type storedRunner struct {
	ID            string    `bson:"_id"`
	Hostname      string    `bson:"hostname"`
	DockerHost    string    `bson:"docker_host"`
	Status        string    `bson:"status"`
	LastHeartbeat time.Time `bson:"last_heartbeat"`
	RegisteredAt  time.Time `bson:"registered_at"`
}

// RunnerSummary is one row of the fleet view: where a runner is, and whether it
// is still reporting.
type RunnerSummary struct {
	ID            string
	Hostname      string
	DockerHost    string
	Status        string
	LastHeartbeat time.Time
	RegisteredAt  time.Time
}

// ListRunners returns the whole fleet, most recently registered first.
func (s *Store) ListRunners() ([]RunnerSummary, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(runnersCollection).Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{
			{Key: "registered_at", Value: -1}, {Key: "_id", Value: 1},
		}))
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []RunnerSummary{}
	for cur.Next(ctx) {
		var doc storedRunner
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode runner: %w", err)
		}
		out = append(out, RunnerSummary{
			ID: doc.ID, Hostname: doc.Hostname, DockerHost: doc.DockerHost,
			Status: doc.Status, LastHeartbeat: doc.LastHeartbeat, RegisteredAt: doc.RegisteredAt,
		})
	}
	return out, cur.Err()
}

// CountOnlineRunners counts the runners the fleet view calls online.
func (s *Store) CountOnlineRunners() (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	count, err := s.db.Collection(runnersCollection).CountDocuments(ctx,
		bson.D{{Key: "status", Value: "online"}})
	if err != nil {
		return 0, fmt.Errorf("count online runners: %w", err)
	}
	return int(count), nil
}
