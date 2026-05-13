package api

import (
	"net/http"
)

func (s *Server) handleFreeze(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	if err := s.freezeProject(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	project, _ := s.store.Get(name)
	writeJSON(w, http.StatusOK, toProjectInfo(project))
}

func (s *Server) handleUnfreeze(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	if err := s.unfreezeProject(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	project, _ := s.store.Get(name)
	writeJSON(w, http.StatusOK, toProjectInfo(project))
}
