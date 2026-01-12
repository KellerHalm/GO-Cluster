package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

type StartRequest struct {
	ID     string   `json:"id"`
	Binary string   `json:"binary"`
	Port   string   `json:"port"`
	Args   []string `json:"args"`
}

type ServiceProcess struct {
	Cmd  *exec.Cmd
	Port string
}

var (
	processes = make(map[string]*ServiceProcess)
	mu        sync.Mutex
)

func startHandler(w http.ResponseWriter, r *http.Request) {
	var req StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mu.Lock()
	defer mu.Unlock()

	if _, exists := processes[req.ID]; exists {
		http.Error(w, "Service already running", http.StatusConflict)
		return
	}

	cwd, _ := os.Getwd()
	binPath := filepath.Join(cwd, req.Binary)
	if _, err := os.Stat(binPath); os.IsNotExist(err) {
		binPath += ".exe"
	}

	args := []string{"-port", req.Port, "-id", req.ID}
	args = append(args, req.Args...)

	cmd := exec.Command(binPath, args...)
	// Отвязываем процесс
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("Failed to start: %v", err), http.StatusInternalServerError)
		return
	}

	processes[req.ID] = &ServiceProcess{Cmd: cmd, Port: req.Port}
	log.Printf("Started service %s on port %s (PID: %d)", req.ID, req.Port, cmd.Process.Pid)

	w.WriteHeader(http.StatusOK)
}

func stopHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")

	mu.Lock()
	defer mu.Unlock()

	proc, exists := processes[id]
	if !exists {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	if err := proc.Cmd.Process.Kill(); err != nil {
		log.Printf("Failed to kill process %s: %v", id, err)
	}

	go proc.Cmd.Wait()

	delete(processes, id)
	log.Printf("Stopped service %s", id)
	w.WriteHeader(http.StatusOK)
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	// Возвращает список запущенных ID для сверки
	mu.Lock()
	defer mu.Unlock()

	active := make(map[string]string)
	for id, proc := range processes {
		// Проверяем жив ли процесс
		active[id] = proc.Port
	}

	json.NewEncoder(w).Encode(active)
}

func main() {
	port := flag.String("port", "9090", "Agent port")
	flag.Parse()

	http.HandleFunc("/start", startHandler)
	http.HandleFunc("/stop", stopHandler)
	http.HandleFunc("/status", statusHandler)

	log.Printf("Agent started on port %s", *port)
	if err := http.ListenAndServe(":"+*port, nil); err != nil {
		log.Fatal(err)
	}
}
