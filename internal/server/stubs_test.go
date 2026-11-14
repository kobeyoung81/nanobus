package server

// Stand-ins for the modules that own the data the bus reads over its internal
// APIs.
//
// The tests seed these instead of writing another module's collection. That is
// the point of the split: a test that inserted a `portal_agents` document would
// keep passing while the product had no way to read it, which is exactly the
// failure this round removes.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/admincatalogue"
	"gitee.com/kobeyoung81/nanobus/internal/portalagents"
	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentStub answers the portal's internal agent reads.
type agentStub struct {
	mu      sync.Mutex
	rows    map[string]portalagents.Agent
	updated map[string]time.Time
}

func newAgentStub(t *testing.T) *agentStub {
	t.Helper()
	return &agentStub{rows: map[string]portalagents.Agent{}, updated: map[string]time.Time{}}
}

func (a *agentStub) put(agent portalagents.Agent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now().UTC()
	if _, ok := a.rows[agent.ID]; !ok {
		a.updated[agent.ID] = now
	}
	a.rows[agent.ID] = agent
}

// touch moves a row's update time, which is what the fleet list sorts on.
func (a *agentStub) touch(id string, at time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updated[id] = at
}

func (a *agentStub) client(t *testing.T) *portalagents.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/v1/agents-fleet" {
			a.mu.Lock()
			out := make([]portalagents.FleetRow, 0, len(a.rows))
			for id, row := range a.rows {
				out = append(out, portalagents.FleetRow{
					ID: row.ID, UserID: row.UserID,
					DisplayName: row.DisplayName, Status: row.Status,
					UpdatedAt: a.updated[id],
				})
			}
			a.mu.Unlock()
			sort.Slice(out, func(i, j int) bool {
				if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
					return out[i].UpdatedAt.After(out[j].UpdatedAt)
				}
				return out[i].ID < out[j].ID
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": out, "count": len(out)})
			return
		}
		const prefix = "/internal/v1/agents/"
		if len(r.URL.Path) > len(prefix) && r.URL.Path[:len(prefix)] == prefix {
			id := r.URL.Path[len(prefix):]
			a.mu.Lock()
			row, ok := a.rows[id]
			a.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"agent not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"agent": row})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return portalagents.New(srv.URL, "test-token")
}

// catalogueStub answers the admin module's internal reads. The default model is
// fixed so a test can assert what an agent list shows; the runtime resolution is
// empty, because a test that needs one is about the catalogue, not about here.
type catalogueStub struct {
	mu        sync.Mutex
	rows      map[string][2]any // agent id -> {model id, model name}
	catalogue map[int64]nanoprotocol.ModelDef
	providers map[int64]nanoprotocol.ModelProviderDef
	visible   map[string]bool // "kind/ref" -> visible
}

func newCatalogueStub(t *testing.T) *catalogueStub {
	t.Helper()
	return &catalogueStub{
		rows:      map[string][2]any{},
		catalogue: map[int64]nanoprotocol.ModelDef{},
		providers: map[int64]nanoprotocol.ModelProviderDef{},
		visible:   map[string]bool{},
	}
}

func (c *catalogueStub) putModel(m nanoprotocol.ModelDef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.catalogue[m.ID] = m
}

func (c *catalogueStub) putProvider(p nanoprotocol.ModelProviderDef) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providers[p.ID] = p
}

func (c *catalogueStub) setModel(agentID string, id int64, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows[agentID] = [2]any{id, name}
}

func (c *catalogueStub) allowVisible(kind, ref string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.visible[kind+"/"+ref] = true
}

func (c *catalogueStub) client(t *testing.T) *admincatalogue.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/internal/agent-default-model":
			var in admincatalogue.RuntimeInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			c.mu.Lock()
			entry, ok := c.rows[in.AgentID]
			c.mu.Unlock()
			if !ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"type_id": "build", "model_id": 0, "model_name": ""})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type_id": "build", "model_id": entry[0], "model_name": entry[1]})
		case r.URL.Path == "/api/v1/internal/capability-visible":
			kind, ref := r.URL.Query().Get("kind"), r.URL.Query().Get("ref")
			c.mu.Lock()
			visible := c.visible[kind+"/"+ref]
			c.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"visible": visible})
		case r.URL.Path == "/api/v1/internal/agent-runtime":
			var in admincatalogue.RuntimeInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			c.mu.Lock()
			entry, ok := c.rows[in.AgentID]
			c.mu.Unlock()
			modelID, modelName := int64(0), ""
			if ok {
				modelID, _ = entry[0].(int64)
				modelName, _ = entry[1].(string)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"default_mode_type_id":    "build",
				"capabilities":            map[string]any{"builtin_tools": []any{}},
				"settings":                map[string]string{},
				"definitions":             []any{},
				"default_mode_model_id":   modelID,
				"default_mode_model_name": modelName,
			})
		case r.URL.Path == "/api/v1/internal/mode-capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"builtin_tools": []any{}})
		case r.URL.Path == "/api/v1/internal/agent-types":
			_ = json.NewEncoder(w).Encode(map[string]any{"types": []any{}})
		case r.URL.Path == "/api/v1/internal/prompt-blocks":
			_ = json.NewEncoder(w).Encode(map[string]any{"head": "", "head_source": "platform", "mode_notes": map[string]string{}})
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/models/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/models/"), 10, 64)
			c.mu.Lock()
			m, ok := c.catalogue[id]
			c.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"model": m})
		case strings.HasPrefix(r.URL.Path, "/api/v1/internal/model-providers/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/model-providers/"), 10, 64)
			c.mu.Lock()
			p, ok := c.providers[id]
			c.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"provider": p})
		default:
			// Every other internal route answers an empty object: these tests are
			// about the bus's own behaviour, not the catalogue's contents.
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return admincatalogue.New(srv.URL, "test-token")
}

// fleetStub answers the provisioner's console reads.
type fleetStub struct {
	mu         sync.Mutex
	runners    []provisionerclient.RunnerSummary
	containers []provisionerclient.ContainerSummary
}

func newFleetStub(t *testing.T) *fleetStub {
	t.Helper()
	return &fleetStub{}
}

func (f *fleetStub) addRunner(row provisionerclient.RunnerSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runners = append(f.runners, row)
}

func (f *fleetStub) addContainer(row provisionerclient.ContainerSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers = append(f.containers, row)
}

func (f *fleetStub) client(t *testing.T) *provisionerclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/internal/console/runners":
			out := f.runners
			sort.SliceStable(out, func(i, j int) bool { return out[i].RegisteredAt.After(out[j].RegisteredAt) })
			_ = json.NewEncoder(w).Encode(map[string]any{"runners": out})
		case "/api/v1/internal/console/runners/counts":
			online := 0
			for _, row := range f.runners {
				if row.Status == "online" {
					online++
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"online": online})
		case "/api/v1/internal/console/containers":
			userID := r.URL.Query().Get("user_id")
			out := make([]provisionerclient.ContainerSummary, 0, len(f.containers))
			for _, row := range f.containers {
				if userID == "" || row.UserID == userID {
					out = append(out, row)
				}
			}
			sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
			_ = json.NewEncoder(w).Encode(map[string]any{"containers": out})
		case "/api/v1/internal/console/containers/counts":
			byStatus := map[string]int{}
			for _, row := range f.containers {
				byStatus[row.Status]++
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"by_status": byStatus})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	return provisionerclient.New(srv.URL, "test-token")
}

// stubTime keeps a fixed clock out of the assertions: rows carry their own times.
func stubTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
