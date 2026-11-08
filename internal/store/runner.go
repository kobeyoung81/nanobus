package store

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// RunnerStaleTimeout is how long a runner may go without a heartbeat before
// it is marked offline by the bus sweeper.
const RunnerStaleTimeout = 2 * time.Minute

const runnersCollection = "runners"

// storedRunner is the document shape. The writes below still build their $set by
// hand (they only ever touch a few fields); this is what the reads decode into.
type storedRunner struct {
	ID            string    `bson:"_id"`
	Hostname      string    `bson:"hostname"`
	DockerHost    string    `bson:"docker_host"`
	Status        string    `bson:"status"`
	LastHeartbeat time.Time `bson:"last_heartbeat"`
	RegisteredAt  time.Time `bson:"registered_at"`
}

// RegisterRunner inserts or updates a runner in the registry. Stale rows for
// runners that are no longer heartbeating are marked offline so the table does
// not accumulate "online" ghosts after a redeploy.
func (s *Store) RegisterRunner(id, hostname, dockerHost string) error {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	// registered_at is $setOnInsert and not $set: it is when the runner was
	// first seen, and the SQL's ON DUPLICATE KEY branch did not touch it either.
	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(runnersCollection).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: id}},
			bson.D{
				{Key: "$set", Value: bson.D{
					{Key: "hostname", Value: hostname},
					{Key: "docker_host", Value: dockerHost},
					{Key: "status", Value: "online"},
					{Key: "last_heartbeat", Value: now},
				}},
				{Key: "$setOnInsert", Value: bson.D{{Key: "registered_at", Value: now}}},
			},
			options.UpdateOne().SetUpsert(true),
		)
		return err
	})
	if err != nil {
		return fmt.Errorf("register runner: %w", err)
	}
	if err := s.MarkStaleRunnersOffline(); err != nil {
		return fmt.Errorf("mark stale runners offline: %w", err)
	}
	return nil
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

// UpdateRunnerHeartbeat touches the last_heartbeat timestamp for a runner.
func (s *Store) UpdateRunnerHeartbeat(id string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// No upsert, and no error for an unknown id: the SQL UPDATE affected zero
	// rows and succeeded.
	_, err := s.db.Collection(runnersCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "last_heartbeat", Value: time.Now().UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("update runner heartbeat: %w", err)
	}
	return nil
}

// MarkStaleRunnersOffline marks runners whose heartbeat is older than the
// staleness window as offline. It never deletes rows — that is left to
// operators via DeleteRunner.
func (s *Store) MarkStaleRunnersOffline() error {
	ctx, cancel := opCtx()
	defer cancel()

	// One updateMany is the whole operation: a single collection, a filter, and
	// no read-then-write, so it needs neither a transaction nor a retry.
	_, err := s.db.Collection(runnersCollection).UpdateMany(ctx,
		bson.D{
			{Key: "status", Value: "online"},
			{Key: "last_heartbeat", Value: bson.D{{Key: "$lt", Value: time.Now().UTC().Add(-RunnerStaleTimeout)}}},
		},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "offline"}}}},
	)
	if err != nil {
		return fmt.Errorf("mark stale runners offline: %w", err)
	}
	return nil
}

// DeleteRunner removes a runner row (used to prune dead runners by hand).
func (s *Store) DeleteRunner(id string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(runnersCollection).DeleteOne(ctx, bson.D{{Key: "_id", Value: id}}); err != nil {
		return fmt.Errorf("delete runner %q: %w", id, err)
	}
	return nil
}
