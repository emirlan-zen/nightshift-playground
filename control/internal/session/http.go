package session

import (
	"encoding/json"
	"errors"
	"net/http"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.service.Status(r.Context()))
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Start(r.Context(), r.URL.Query().Get("c"), r.URL.Query().Get("slot"))
	if err != nil {
		writeError(w, result.Result, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Stop(r.Context(), r.URL.Query().Get("c"), r.URL.Query().Get("instance"))
	if err != nil {
		writeError(w, result.Result, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) Action(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := h.service.Action(r.Context(), action, r.URL.Query().Get("c"))
		if err != nil {
			writeError(w, result.Result, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeError(w http.ResponseWriter, output string, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, ErrUnknownAgent) || errors.Is(err, ErrBadSlot) || errors.Is(err, ErrBadInstance) {
		status = http.StatusBadRequest
	}
	if output != "" {
		http.Error(w, output+"\n"+err.Error(), status)
		return
	}
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
