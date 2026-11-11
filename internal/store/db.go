// Package store provides MongoDB-backed storage for the bus service, including
// the user registry, the agent/skill registry and the message queues.
//
// Every domain is in MongoDB, and MongoDB is the only store: the strangler phase
// this package went through — two handles, one domain at a time — is over, and so
// is the MySQL side. `Store` holds a MongoDB client and nothing else.
package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Store provides access to the platform database.
type Store struct {
	mongo *mongo.Client
	db    *mongo.Database
	// readOnlyKeys supplies the permission catalog's read-only keys. They live
	// in the admin module; the bus asks over HTTP.
	readOnlyKeys func() (map[string]nanoprotocol.PermissionAction, error)
	// accounts is the account domain's owner — the auth module. The bus keeps no
	// handle for `auth_users`; it asks (accounts_remote.go).
	accounts AccountBackend
	// containers is the provisioning queue, which onboarding asks for a new
	// account's container.
	containers ContainerEnqueuer
}

// Open connects to MongoDB and returns a Store. It pings the server so a bad URI
// (or bad credentials) fails at startup rather than on the first request.
func Open(mongoURI string) (*Store, error) {
	if mongoURI == "" {
		return nil, errors.New("mongo uri is required")
	}
	name, err := databaseNameOf(mongoURI)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI))
	if err != nil {
		return nil, fmt.Errorf("connect mongo: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}

	return &Store{mongo: client, db: client.Database(name)}, nil
}

// databaseNameOf extracts the database name from a MongoDB URI's path.
//
// The path is the only place the driver's own default-database mechanism reads
// from, and `Database("")` would not fall back to it — so an unnamed URI is an
// error rather than a guess.
func databaseNameOf(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongo uri: %w", err)
	}
	name := strings.Trim(u.Path, "/")
	if name == "" {
		return "", errors.New("mongo uri must name a database, e.g. mongodb://host:27017/nanobus")
	}
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return "", fmt.Errorf("mongo uri names more than one database: %q", name)
	}
	return name, nil
}

// NewMongo wraps an existing MongoDB client and database name.
func NewMongo(client *mongo.Client, database string) *Store {
	return &Store{mongo: client, db: client.Database(database)}
}

// Close releases the client.
func (s *Store) Close() error {
	if s.mongo == nil {
		return nil
	}
	return s.mongo.Disconnect(context.Background())
}

// DB returns the underlying MongoDB database.
func (s *Store) DB() *mongo.Database {
	return s.db
}

// opTimeout bounds a single database operation.
//
// The SQL methods these replaced took no context, and adding one to every
// signature would ripple through the server, the HTTP layer and every test. So
// each method derives its own bounded context instead — which has the useful
// side effect that a hung server surfaces as an error rather than as a request
// that never returns.
const opTimeout = 15 * time.Second

func opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// retryOnDuplicateUpsert runs fn again once when it fails with a duplicate-key
// error. MongoDB documents that concurrent upserts on a unique index can raise
// E11000 instead of updating in place; the documented remedy is a retry, and
// without it a race turns into a user-visible failure.
func retryOnDuplicateUpsert(fn func() error) error {
	err := fn()
	if err != nil && mongo.IsDuplicateKeyError(err) {
		return fn()
	}
	return err
}
