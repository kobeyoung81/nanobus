package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// The prompt blocks of the session agents. The **shared head** is stored on its own
// (session_head_prompts, keyed without a mode, so build and plan cannot diverge);
// the **mode switch notes** still live on the agent types, one prompt each.

// promptBlocksResponse is the shape both planes read.
type promptBlocksResponse struct {
	Head      *store.SessionHeadPrompt `json:"head"`
	ModeNotes map[string]string        `json:"mode_notes"`
}

// modeNoteIDs are the session types whose prompt is a mode switch note.
var modeNoteIDs = []string{"plan", "build"}

func (s *Server) modeNotes() (map[string]string, error) {
	notes := make(map[string]string, len(modeNoteIDs))
	for _, id := range modeNoteIDs {
		t, err := s.store.GetAgentType(id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			notes[id] = t.Prompt
		}
	}
	return notes, nil
}

// restGetPromptBlocks (admin) returns the platform prompt blocks.
func (s *Server) restGetPromptBlocks(w http.ResponseWriter, r *http.Request) {
	head, err := s.store.PlatformSessionHead()
	if err != nil {
		slog.Error("get session head prompt", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	notes, err := s.modeNotes()
	if err != nil {
		slog.Error("get mode notes", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, promptBlocksResponse{Head: head, ModeNotes: notes})
}

// restSavePromptBlock (admin) upserts one platform block:
//
//	{"block": "session-head", "content": "…"}
//
// The mode switch notes are edited through the agent type prompt they live on
// (PUT /api/v1/agent-types/{id}), so they are rejected here to keep one writer per
// value.
func (s *Server) restSavePromptBlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Block   string `json:"block"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Block != "session-head" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown prompt block: the mode switch notes are edited on the agent type",
		})
		return
	}
	if err := s.store.SaveSessionHead(store.PromptScopePlatform, "", body.Content); err != nil {
		slog.Error("save session head prompt", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("session_head_prompt")
	s.recordAudit(r, "prompt_block.save", "session_head_prompt", "platform", nil)
	writeJSON(w, http.StatusOK, map[string]string{"block": body.Block, "status": "saved"})
}

// restGetAgentPromptBlocks (user plane, read-only) resolves what one agent actually
// runs with: the effective shared head (its own override when one exists, else the
// platform's) and the two mode notes.
func (s *Server) restGetAgentPromptBlocks(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if !s.agentOwnedBy(agentID, userID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	platform, err := s.store.PlatformSessionHead()
	if err != nil {
		slog.Error("get session head prompt", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	own, err := s.store.AgentSessionHead(agentID)
	if err != nil {
		slog.Error("get agent session head prompt", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	head, source := platform, "platform"
	if own != nil && own.Content != "" {
		head, source = own, "agent"
	}
	notes, err := s.modeNotes()
	if err != nil {
		slog.Error("get mode notes", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":    agentID,
		"head":        head,
		"head_source": source,
		"mode_notes":  notes,
	})
}
