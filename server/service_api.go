package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
)

const (
	serviceOwnerHeader = "X-ARTEX-Owner-ID"
	maxServiceBody     = 64 << 10
)

var serviceOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@-]{0,199}$`)

func (s *Server) requireServiceAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.serviceToken == "" {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if len(token) != len(s.serviceToken) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(s.serviceToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		owner := strings.TrimSpace(r.Header.Get(serviceOwnerHeader))
		if !serviceOwnerPattern.MatchString(owner) {
			writeErr(w, http.StatusBadRequest, "Invalid owner identity")
			return
		}
		r.Header.Set(serviceOwnerHeader, owner)
		next.ServeHTTP(w, r)
	})
}

func serviceOwner(r *http.Request) string {
	return r.Header.Get(serviceOwnerHeader)
}

func decodeServiceJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxServiceBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func (s *Server) registerServiceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/service/health", s.serviceHealth)

	mux.HandleFunc("GET /api/service/tasks", s.serviceListTasks)
	mux.HandleFunc("POST /api/service/tasks", s.serviceCreateTask)
	mux.HandleFunc("GET /api/service/tasks/{id}", s.serviceGetTask)
	mux.HandleFunc("PATCH /api/service/tasks/{id}", s.serviceUpdateTask)
	mux.HandleFunc("POST /api/service/tasks/{id}/control", s.serviceControlTask)
	mux.HandleFunc("DELETE /api/service/tasks/{id}", s.serviceDeleteTask)

	mux.HandleFunc("GET /api/service/traffic", s.serviceTraffic)
	mux.HandleFunc("GET /api/service/traffic/hosts", s.serviceTrafficHosts)
	mux.HandleFunc("GET /api/service/traffic/exchange", s.serviceTrafficExchange)
	mux.HandleFunc("GET /api/service/traffic/blob", s.serviceTrafficBlob)

	mux.HandleFunc("GET /api/service/commands", s.serviceCommands)
	mux.HandleFunc("GET /api/service/commands/stats", s.serviceCommandStats)

	mux.HandleFunc("GET /api/service/llm-records", s.serviceLLMRecords)
	mux.HandleFunc("GET /api/service/llm-records/tasks", s.serviceLLMTasks)
	mux.HandleFunc("GET /api/service/llm-records/{id}", s.serviceLLMRecord)
}

func (s *Server) serviceHealth(w http.ResponseWriter, r *http.Request) {
	dbOK := s.m != nil && s.m.pg != nil
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            dbOK,
		"service":       "artex",
		"version":       BuildVersion,
		"tenantScoped":  true,
		"databaseReady": dbOK,
	})
}

func (s *Server) serviceOwnedTask(r *http.Request) (*Task, bool) {
	id, ok := canonicalTaskID(r.PathValue("id"))
	if !ok {
		return nil, false
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	owned, err := s.m.pg.GetTaskForOwner(n, serviceOwner(r))
	if err != nil || owned == nil {
		return nil, false
	}
	if task, ok := s.m.Task(id); ok && task.OwnerID == serviceOwner(r) {
		return task, true
	}
	return nil, false
}

func (s *Server) serviceListTasks(w http.ResponseWriter, r *http.Request) {
	list := s.m.ListForOwner(serviceOwner(r))
	metrics, err := s.m.PG().TaskListMetricsAll()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Engine metrics are unavailable")
		return
	}
	archiveBlockers, _ := s.m.PG().TaskArchiveBlockers()
	dtos := make([]TaskDTO, 0, len(list))
	for _, task := range list {
		dto := taskDTO(task, s.resolvedTaskStatus(task))
		applyTaskArchiveBlocker(&dto, archiveBlockers)
		s.applyTaskMetrics(&dto, task, metrics[task.ExpID])
		dtos = append(dtos, dto)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": dtos, "active": ""})
}

type serviceTaskCreate struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	Goal            string `json:"goal"`
	ClientRequestID string `json:"clientRequestId"`
	TimeoutSeconds  int    `json:"timeoutSeconds,omitempty"`
}

func (s *Server) serviceCreateTask(w http.ResponseWriter, r *http.Request) {
	var request serviceTaskCreate
	if err := decodeServiceJSON(w, r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid task request")
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Description = strings.TrimSpace(request.Description)
	request.Goal = strings.TrimSpace(request.Goal)
	request.ClientRequestID = strings.TrimSpace(request.ClientRequestID)
	if request.Name == "" || request.Goal == "" || request.ClientRequestID == "" {
		writeErr(w, http.StatusBadRequest, "name, goal and clientRequestId are required")
		return
	}
	if len(request.Name) > 240 || len(request.Goal) > 64_000 || len(request.Description) > 64_000 ||
		len(request.ClientRequestID) > 200 {
		writeErr(w, http.StatusBadRequest, "Task fields exceed their limits")
		return
	}
	if request.Description == "" {
		request.Description = request.Name
	}
	if err := goalGateCheck(request.Goal); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.TimeoutSeconds < 0 {
		request.TimeoutSeconds = 0
	}
	owner := serviceOwner(r)
	if existing, err := s.m.PG().GetTaskByClientRequestID(owner, request.ClientRequestID); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Unable to check task idempotency")
		return
	} else if existing != nil {
		if task, ok := s.m.Task(strconv.FormatInt(existing.ID, 10)); ok {
			writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
			return
		}
	}
	task, err := s.m.CreateTaskWithOptions(request.Description, request.Goal, db.TaskCreateOptions{
		OwnerID: owner, ClientRequestID: request.ClientRequestID,
		Name: request.Name, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to create the engine task")
		return
	}
	s.launchTask(task, request.Description+" "+request.Goal, true)
	writeJSON(w, http.StatusCreated, taskDTO(task, s.resolvedTaskStatus(task)))
}

func (s *Server) serviceGetTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.serviceOwnedTask(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}

type serviceTaskUpdate struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Goal        *string `json:"goal"`
}

func (s *Server) serviceUpdateTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.serviceOwnedTask(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	var request serviceTaskUpdate
	if err := decodeServiceJSON(w, r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid task update")
		return
	}
	if request.Name != nil {
		value := strings.TrimSpace(*request.Name)
		if value == "" || len(value) > 240 {
			writeErr(w, http.StatusBadRequest, "Invalid task name")
			return
		}
		request.Name = &value
	}
	if request.Description != nil {
		value := strings.TrimSpace(*request.Description)
		if len(value) > 64_000 {
			writeErr(w, http.StatusBadRequest, "Task description is too long")
			return
		}
		request.Description = &value
	}
	if request.Goal != nil {
		value := strings.TrimSpace(*request.Goal)
		if len(value) > 64_000 {
			writeErr(w, http.StatusBadRequest, "Task goal is too long")
			return
		}
		if err := goalGateCheck(value); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		request.Goal = &value
	}
	n, _ := strconv.ParseInt(task.ID, 10, 64)
	updated, err := s.m.PG().UpdateTaskForOwner(n, serviceOwner(r), request.Name, request.Description, request.Goal)
	if err != nil || updated == nil {
		writeErr(w, http.StatusInternalServerError, "Unable to update the engine task")
		return
	}
	if request.Name != nil {
		_, _ = s.m.UpdateTaskMetadata(task.ID, db.TaskPatch{Name: request.Name})
	}
	if request.Description != nil {
		task.Description = *request.Description
	}
	if request.Goal != nil {
		task.Goal = *request.Goal
	}
	writeJSON(w, http.StatusOK, taskDTO(task, s.resolvedTaskStatus(task)))
}

func (s *Server) serviceControlTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.serviceOwnedTask(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	var request struct {
		Action string `json:"action"`
	}
	if err := decodeServiceJSON(w, r, &request); err != nil {
		writeErr(w, http.StatusBadRequest, "Invalid control request")
		return
	}
	if request.Action != "pause" && request.Action != "resume" {
		writeErr(w, http.StatusBadRequest, "action must be pause or resume")
		return
	}
	result, err := s.applyTaskControl(task, request.Action)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) serviceDeleteTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.serviceOwnedTask(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServiceBody)
	s.deleteTaskByID(w, r, task.ID)
}

func (s *Server) serviceTrafficStore(w http.ResponseWriter, r *http.Request) *traffic.Traffic {
	store, err := s.m.TrafficForOwner(serviceOwner(r))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "Traffic recorder is unavailable")
		return nil
	}
	return store
}

func (s *Server) serviceTraffic(w http.ResponseWriter, r *http.Request) {
	store := s.serviceTrafficStore(w, r)
	if store == nil {
		return
	}
	q := r.URL.Query()
	page := atoiDefault(q.Get("page"), 0)
	size := atoiDefault(q.Get("size"), 100)
	ex, matched, err := store.Page(traffic.PageQuery{
		Host: q.Get("host"), Method: q.Get("method"), Query: q.Get("q"),
		Body: q.Get("body"), Path: q.Get("path"), Status: q.Get("status"),
		RespMin: int64(atoiDefault(q.Get("resp_min"), -1)),
		RespMax: int64(atoiDefault(q.Get("resp_max"), -1)),
		Sort:    q.Get("sort"), Order: q.Get("order"),
	}, page, size)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read traffic")
		return
	}
	count, _ := store.Count()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "count": count, "total": matched, "page": page, "size": size,
		"exchanges": trafficDTOs(ex),
	})
}

func (s *Server) serviceTrafficHosts(w http.ResponseWriter, r *http.Request) {
	store := s.serviceTrafficStore(w, r)
	if store == nil {
		return
	}
	hosts, err := store.Hosts()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read traffic hosts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts})
}

func (s *Server) serviceTrafficExchange(w http.ResponseWriter, r *http.Request) {
	store := s.serviceTrafficStore(w, r)
	if store == nil {
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "Missing exchange id")
		return
	}
	request, response, err := store.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Traffic exchange not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"req": request, "resp": response})
}

func (s *Server) serviceTrafficBlob(w http.ResponseWriter, r *http.Request) {
	store := s.serviceTrafficStore(w, r)
	if store == nil {
		return
	}
	hash := strings.TrimSpace(r.URL.Query().Get("hash"))
	if hash == "" {
		writeErr(w, http.StatusBadRequest, "Missing blob hash")
		return
	}
	file, size, err := store.Blob(hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, "Traffic blob not found")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", hash+".bin"))
	_, _ = io.Copy(w, file)
}

func (s *Server) serviceTaskFilter(r *http.Request) (*int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("task"))
	if raw == "" {
		return nil, true
	}
	id, ok := canonicalTaskID(raw)
	if !ok {
		return nil, false
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	task, err := s.m.PG().GetTaskForOwner(n, serviceOwner(r))
	if err != nil || task == nil {
		return nil, false
	}
	return &n, true
}

func (s *Server) serviceCommands(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.serviceTaskFilter(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	q := r.URL.Query()
	commands, total, err := s.m.PG().ListCommandsForOwner(serviceOwner(r), taskID,
		q.Get("q"), atoiDefault(q.Get("page"), 0), atoiDefault(q.Get("size"), 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read command records")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": commands, "total": total})
}

func (s *Server) serviceCommandStats(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.serviceTaskFilter(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	stats, err := s.m.PG().ToolStatsForOwner(serviceOwner(r), taskID, r.URL.Query().Get("q"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read command stats")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stats": stats})
}

func (s *Server) serviceLLMRecords(w http.ResponseWriter, r *http.Request) {
	taskID, ok := s.serviceTaskFilter(r)
	if !ok {
		writeErr(w, http.StatusNotFound, "Task not found")
		return
	}
	q := r.URL.Query()
	task := q.Get("task")
	if taskID == nil {
		task = ""
	}
	records, total, err := s.m.PG().ListLLMRecordsForOwner(serviceOwner(r), q.Get("model"),
		q.Get("session"), task, atoiDefault(q.Get("page"), 0), atoiDefault(q.Get("size"), 50))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read LLM records")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": records, "total": total})
}

func (s *Server) serviceLLMTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.m.PG().LLMTasksForOwner(serviceOwner(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "Unable to read LLM task records")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) serviceLLMRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "Invalid record id")
		return
	}
	record, err := s.m.PG().GetLLMRecordForOwner(id, serviceOwner(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "LLM record not found")
		return
	}
	writeJSON(w, http.StatusOK, record)
}
