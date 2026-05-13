package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type BatchRequest struct {
	Operations []BatchOp `json:"operations"`
}

type BatchOp struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	Force   bool   `json:"force,omitempty"`
}

type BatchResult struct {
	Action  string `json:"action"`
	Project string `json:"project"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	var req BatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if len(req.Operations) == 0 {
		writeError(w, http.StatusBadRequest, "no operations provided")
		return
	}

	results := make([]BatchResult, 0, len(req.Operations))
	for _, op := range req.Operations {
		results = append(results, s.executeBatchOp(op))
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

func (s *Server) executeBatchOp(op BatchOp) BatchResult {
	if op.Project == "" {
		return BatchResult{Action: op.Action, Status: "error", Message: "project name required"}
	}
	switch op.Action {
	case "freeze":
		if err := s.freezeProject(op.Project); err != nil {
			return BatchResult{Action: "freeze", Project: op.Project, Status: "error", Message: err.Error()}
		}
		return BatchResult{Action: "freeze", Project: op.Project, Status: "ok"}
	case "unfreeze":
		if err := s.unfreezeProject(op.Project); err != nil {
			return BatchResult{Action: "unfreeze", Project: op.Project, Status: "error", Message: err.Error()}
		}
		return BatchResult{Action: "unfreeze", Project: op.Project, Status: "ok"}
	case "destroy":
		if err := s.destroyProjectByName(op.Project); err != nil {
			return BatchResult{Action: "destroy", Project: op.Project, Status: "error", Message: err.Error()}
		}
		return BatchResult{Action: "destroy", Project: op.Project, Status: "ok"}
	case "status":
		project, exists := s.store.Get(op.Project)
		if !exists {
			return BatchResult{Action: "status", Project: op.Project, Status: "not_found"}
		}
		return BatchResult{Action: "status", Project: op.Project, Status: string(project.Status), Message: fmt.Sprintf("%d service(s)", len(project.Services))}
	case "restart":
		if err := s.freezeProject(op.Project); err != nil {
			return BatchResult{Action: "restart", Project: op.Project, Status: "error", Message: "freeze: " + err.Error()}
		}
		if err := s.unfreezeProject(op.Project); err != nil {
			return BatchResult{Action: "restart", Project: op.Project, Status: "error", Message: "unfreeze: " + err.Error()}
		}
		return BatchResult{Action: "restart", Project: op.Project, Status: "ok"}
	default:
		return BatchResult{Action: op.Action, Project: op.Project, Status: "error", Message: fmt.Sprintf("unknown action %q", op.Action)}
	}
}
