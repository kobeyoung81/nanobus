package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func doJSON(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRestAgentGroups_CreateAndList(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	mux := restMux(t, s)

	rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"工作"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Position int    `json:"position"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.ID == "" || created.Name != "工作" || created.Position != 0 {
		t.Fatalf("created = %+v", created)
	}

	rec = doJSON(t, mux, http.MethodGet, "/api/v1/agent-groups?user_id=user-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
	}
	var listed struct {
		Groups []struct {
			ID string `json:"id"`
		} `json:"groups"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Count != 1 || len(listed.Groups) != 1 || listed.Groups[0].ID != created.ID {
		t.Fatalf("listed = %+v", listed)
	}

	// An empty list is still an array, never null: the sidebar reads .length.
	rec = doJSON(t, mux, http.MethodGet, "/api/v1/agent-groups?user_id=user-2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("empty list = %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"groups":[]`)) {
		t.Fatalf("empty list is not an array: %s", rec.Body.String())
	}
}

func TestRestAgentGroups_Validation(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	storetest.SeedUser(t, s.store, "user-2", "pro", 2)
	mux := restMux(t, s)

	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"工作"}`); rec.Code != http.StatusCreated {
		t.Fatalf("seed group = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"工作"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank name = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"name":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing user = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestAgentGroups_RenameDeleteReorder(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	storetest.SeedUser(t, s.store, "user-2", "pro", 2)
	mux := restMux(t, s)

	created := []string{}
	for _, name := range []string{"A", "B", "C"} {
		rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"`+name+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create %s = %d: %s", name, rec.Code, rec.Body.String())
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		created = append(created, body.ID)
	}

	// Rename, then a cross-user rename is a miss.
	if rec := doJSON(t, mux, http.MethodPut, "/api/v1/agent-groups/"+created[0], `{"user_id":"user-1","name":"项目"}`); rec.Code != http.StatusOK {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPut, "/api/v1/agent-groups/"+created[0], `{"user_id":"user-2","name":"偷"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user rename = %d: %s", rec.Code, rec.Body.String())
	}

	// Reorder: reversed.
	order, _ := json.Marshal(map[string]any{"user_id": "user-1", "ids": []string{created[2], created[1], created[0]}})
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups/reorder", string(order)); rec.Code != http.StatusOK {
		t.Fatalf("reorder = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups/reorder", `{"user_id":"user-1","ids":["nope"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad reorder = %d: %s", rec.Code, rec.Body.String())
	}

	// Delete one group: agents in it become ungrouped.
	seedAgent(t, s, "agent-1", "user-1", "active")
	if err := s.store.SetAgentGroup("agent-1", created[2]); err != nil {
		t.Fatalf("SetAgentGroup: %v", err)
	}
	rec := doJSON(t, mux, http.MethodDelete, "/api/v1/agent-groups/"+created[2]+"?user_id=user-1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	var deleted struct {
		Unassigned int64 `json:"unassigned"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if deleted.Unassigned != 1 {
		t.Fatalf("unassigned = %d, want 1", deleted.Unassigned)
	}
	if rec := doJSON(t, mux, http.MethodDelete, "/api/v1/agent-groups/"+created[2]+"?user_id=user-1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestAgents_GroupOnCreateAndUpdate(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	storetest.SeedUser(t, s.store, "user-2", "pro", 2)
	mux := restMux(t, s)

	rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"工作"}`)
	var group struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &group); err != nil {
		t.Fatalf("decode group: %v", err)
	}

	// Create in the group.
	body := `{"user_id":"user-1","id":"agent-1","display_name":"A","group_id":"` + group.ID + `"}`
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agents", body); rec.Code != http.StatusCreated {
		t.Fatalf("create with group = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, mux, http.MethodGet, "/api/v1/agents?user_id=user-1", "")
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"group_id":"`+group.ID+`"`)) {
		t.Fatalf("list does not carry the group: %s", rec.Body.String())
	}

	// Someone else's group is refused.
	body = `{"user_id":"user-2","id":"agent-2","display_name":"B","group_id":"` + group.ID + `"}`
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agents", body); rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-user group on create = %d: %s", rec.Code, rec.Body.String())
	}

	// Move it, then clear it. Absent group_id leaves it alone.
	rec = doJSON(t, mux, http.MethodPut, "/api/v1/agents/agent-1", `{"group_id":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear group = %d: %s", rec.Code, rec.Body.String())
	}
	a, err := s.store.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != "" {
		t.Fatalf("after clear: %+v, %v", a, err)
	}
	if rec := doJSON(t, mux, http.MethodPut, "/api/v1/agents/agent-1", `{"display_name":"A2"}`); rec.Code != http.StatusOK {
		t.Fatalf("update without group = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPut, "/api/v1/agents/agent-1", `{"group_id":"`+group.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("set group = %d: %s", rec.Code, rec.Body.String())
	}
	a, err = s.store.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != group.ID {
		t.Fatalf("after set: %+v, %v", a, err)
	}
}

func TestHandlerAgentGroupOverWS(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)

	group, err := s.store.CreateAgentGroup("user-1", "工作")
	if err != nil {
		t.Fatalf("CreateAgentGroup: %v", err)
	}

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeCreateAgent, nanoprotocol.CreateAgentRequest{
		UserID: "user-1", ID: "agent-1", DisplayName: "A", GroupID: group.ID,
	})
	created := s.handleCreateAgent(msg)
	if created == nil || created.Type == nanoprotocol.TypeError {
		t.Fatalf("create over ws: %+v", created)
	}
	a, err := s.store.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != group.ID {
		t.Fatalf("ws create grouping: %+v, %v", a, err)
	}

	empty := ""
	msg, _ = nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgent, nanoprotocol.UpdateAgentRequest{
		ID: "agent-1", GroupID: &empty,
	})
	updated := s.handleUpdateAgent(msg)
	if updated == nil || updated.Type == nanoprotocol.TypeError {
		t.Fatalf("update over ws: %+v", updated)
	}
	a, err = s.store.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != "" {
		t.Fatalf("ws update grouping: %+v, %v", a, err)
	}
}

func TestRestAgentGroups_OrderAndArrange(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	storetest.SeedUser(t, s.store, "user-2", "pro", 2)
	mux := restMux(t, s)

	groupIDs := []string{}
	for _, name := range []string{"工作", "个人", "空"} {
		rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups", `{"user_id":"user-1","name":"`+name+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create group %s = %d: %s", name, rec.Code, rec.Body.String())
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode group: %v", err)
		}
		groupIDs = append(groupIDs, body.ID)
	}

	for _, spec := range []struct {
		id, group string
	}{
		{"a1", groupIDs[0]}, {"a2", groupIDs[0]}, {"a3", groupIDs[0]},
		{"b1", groupIDs[1]},
		{"u1", ""},
	} {
		body := `{"user_id":"user-1","id":"` + spec.id + `","display_name":"` + spec.id + `"`
		if spec.group != "" {
			body += `,"group_id":"` + spec.group + `"`
		}
		body += `}`
		if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agents", body); rec.Code != http.StatusCreated {
			t.Fatalf("create agent %s = %d: %s", spec.id, rec.Code, rec.Body.String())
		}
	}

	// A second user's agent, used below to prove arrange is scoped to the caller.
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agents",
		`{"user_id":"user-2","id":"c1","display_name":"c1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create user-2 agent = %d: %s", rec.Code, rec.Body.String())
	}

	type listed struct {
		Groups []struct {
			ID     string   `json:"id"`
			Agents []string `json:"agents"`
		} `json:"groups"`
		Ungrouped []string `json:"ungrouped"`
	}
	read := func() listed {
		t.Helper()
		rec := doJSON(t, mux, http.MethodGet, "/api/v1/agent-groups?user_id=user-1", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d: %s", rec.Code, rec.Body.String())
		}
		var body listed
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return body
	}

	first := read()
	if len(first.Groups) != 3 {
		t.Fatalf("groups = %+v", first.Groups)
	}
	if !sameList(first.Groups[0].Agents, []string{"a1", "a2", "a3"}) {
		t.Fatalf("work order = %v", first.Groups[0].Agents)
	}
	if !sameList(first.Groups[1].Agents, []string{"b1"}) {
		t.Fatalf("personal order = %v", first.Groups[1].Agents)
	}
	// An empty group is an empty array, never null: the sidebar reads .length.
	if first.Groups[2].Agents == nil || len(first.Groups[2].Agents) != 0 {
		t.Fatalf("empty group agents = %#v", first.Groups[2].Agents)
	}
	if !sameList(first.Ungrouped, []string{"u1"}) {
		t.Fatalf("ungrouped = %v", first.Ungrouped)
	}

	arranged := `{"user_id":"user-1","groups":[{"id":"` + groupIDs[0] + `","agents":["a3","a1","u1"]},{"id":"` + groupIDs[1] + `","agents":["b1","a2"]}],"ungrouped":[]}`
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups/arrange", arranged); rec.Code != http.StatusOK {
		t.Fatalf("arrange = %d: %s", rec.Code, rec.Body.String())
	}
	after := read()
	if !sameList(after.Groups[0].Agents, []string{"a3", "a1", "u1"}) {
		t.Fatalf("work order after arrange = %v", after.Groups[0].Agents)
	}
	if !sameList(after.Groups[1].Agents, []string{"b1", "a2"}) {
		t.Fatalf("personal order after arrange = %v", after.Groups[1].Agents)
	}
	if len(after.Ungrouped) != 0 {
		t.Fatalf("ungrouped after arrange = %v", after.Ungrouped)
	}

	// Another user's agent is refused and nothing moves.
	crossUser := `{"user_id":"user-1","groups":[{"id":"` + groupIDs[0] + `","agents":["a1","c1"]}]}`
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups/arrange", crossUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("cross-user arrange = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, mux, http.MethodPost, "/api/v1/agent-groups/arrange", `{"groups":[]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing user = %d: %s", rec.Code, rec.Body.String())
	}
}

func sameList(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
