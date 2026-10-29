package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"smart-iv-backend/models"
	"smart-iv-backend/sim"
)

type Server struct {
	engine *sim.Engine
}

func NewServer(engine *sim.Engine) http.Handler {
	s := &Server{engine: engine}
	return s.cors(http.HandlerFunc(s.handle))
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set(
			"Access-Control-Allow-Methods",
			"GET, POST, PATCH, OPTIONS",
		)
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Content-Type, Authorization",
		)

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")

	if path == "" {
		path = "/"
	}

	switch path {
	case "/":
		writeJSON(w, http.StatusOK, map[string]string{
			"message": "Smart IV Monitoring Simulation API",
		})
		return

	case "/health":
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
		})
		return

	case "/api/state":
		writeJSON(w, http.StatusOK, s.engine.Snapshot())
		return

	case "/api/devices":
		writeJSON(w, http.StatusOK, s.engine.Snapshot().Devices)
		return

	case "/api/settings":
		s.handleSettings(w, r)
		return

	case "/api/simulation/start":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		s.engine.Start()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return

	case "/api/simulation/pause":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		s.engine.Pause()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return

	case "/api/simulation/reset":
		if !requireMethod(w, r, http.MethodPost) {
			return
		}
		s.engine.Reset()
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return

	case "/api/simulation/speed":
		s.handleSpeed(w, r)
		return

	case "/api/reports/events.csv":
		s.handleEventsCSV(w, r)
		return
	}

	if strings.HasPrefix(path, "/api/devices/") {
		s.handleDevice(w, r, path)
		return
	}

	if strings.HasPrefix(path, "/api/alerts/") {
		s.handleAlert(w, r, path)
		return
	}

	if strings.HasPrefix(path, "/api/scenarios/") {
		s.handleScenario(w, r, path)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleDevice(
	w http.ResponseWriter,
	r *http.Request,
	path string,
) {
	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) < 3 || parts[0] != "api" || parts[1] != "devices" {
		http.NotFound(w, r)
		return
	}

	id := parts[2]

	if len(parts) == 3 {
		if !requireMethod(w, r, http.MethodGet) {
			return
		}

		device, ok := s.engine.GetDevice(id)
		if !ok {
			writeError(w, http.StatusNotFound, "Device not found")
			return
		}

		writeJSON(w, http.StatusOK, device)
		return
	}

	if len(parts) == 4 && parts[3] == "control" {
		if !requireMethod(w, r, http.MethodPatch) {
			return
		}

		var payload struct {
			TargetDropRate *float64 `json:"targetDropRate"`
			ServoAngle     *float64 `json:"servoAngle"`
		}

		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}

		if payload.TargetDropRate == nil && payload.ServoAngle == nil {
			writeError(w, http.StatusBadRequest, "No control values provided")
			return
		}

		if err := s.engine.ControlDevice(
			id,
			payload.TargetDropRate,
			payload.ServoAngle,
		); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		device, _ := s.engine.GetDevice(id)
		writeJSON(w, http.StatusOK, device)
		return
	}

	http.NotFound(w, r)
}

func (s *Server) handleSpeed(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	var payload struct {
		Speed float64 `json:"speed"`
	}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	switch payload.Speed {
	case 1, 10, 60:
		s.engine.SetSpeed(models.SimSpeed(payload.Speed))
	default:
		writeError(
			w,
			http.StatusBadRequest,
			"Speed must be 1, 10, or 60",
		)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"speed":   payload.Speed,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.engine.GetSettings())

	case http.MethodPatch:
		var incoming map[string]json.RawMessage

		if err := json.NewDecoder(r.Body).Decode(&incoming); err != nil {
			writeError(w, http.StatusBadRequest, "Invalid JSON body")
			return
		}

		settings := s.engine.GetSettings()

		if err := mergeSettings(incoming, &settings); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		s.engine.UpdateSettings(settings)
		writeJSON(w, http.StatusOK, s.engine.GetSettings())

	default:
		methodNotAllowed(w)
	}
}

func mergeSettings(
	incoming map[string]json.RawMessage,
	settings *models.SimSettings,
) error {
	data, err := json.Marshal(settings)
	if err != nil {
		return err
	}

	current := make(map[string]json.RawMessage)

	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}

	for key, value := range incoming {
		current[key] = value
	}

	merged, err := json.Marshal(current)
	if err != nil {
		return err
	}

	return json.Unmarshal(merged, settings)
}

func (s *Server) handleAlert(
	w http.ResponseWriter,
	r *http.Request,
	path string,
) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) != 4 || parts[3] != "ack" {
		http.NotFound(w, r)
		return
	}

	id := parts[2]

	if !s.engine.AckAlert(id) {
		writeError(w, http.StatusNotFound, "Alert not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"id":      id,
	})
}

func (s *Server) handleScenario(
	w http.ResponseWriter,
	r *http.Request,
	path string,
) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")

	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	name := parts[2]
	deviceID := r.URL.Query().Get("deviceId")

	if err := s.engine.ApplyScenario(name, deviceID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"scenario": name,
	})
}

func (s *Server) handleEventsCSV(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set(
		"Content-Disposition",
		`attachment; filename="events.csv"`,
	)

	writer := csv.NewWriter(w)
	defer writer.Flush()

	if err := writer.Write([]string{
		"Timestamp",
		"Device ID",
		"Event Type",
		"Severity",
		"Message",
	}); err != nil {
		return
	}

	for _, event := range s.engine.GetEvents() {
		record := []string{
			event.Timestamp.Format(time.RFC3339),
			event.DeviceID,
			event.EventType,
			event.Severity,
			event.Message,
		}

		if err := writer.Write(record); err != nil {
			return
		}
	}
}

func requireMethod(
	w http.ResponseWriter,
	r *http.Request,
	method string,
) bool {
	if r.Method != method {
		methodNotAllowed(w)
		return false
	}
	return true
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST, PATCH, OPTIONS")
	writeError(w, http.StatusMethodNotAllowed, "Method not allowed")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{
		"error": message,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(value); err != nil {
		fmt.Println("Failed to encode response:", err)
	}
}

var _ = strconv.Itoa