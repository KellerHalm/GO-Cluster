package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Конфигурация сервиса
type ServiceConfig struct {
	BinaryName string   `json:"binary_name"`
	Args       []string `json:"args"`
}

type Replica struct {
	ID        string    `json:"id"`
	NodeURL   string    `json:"node_url"`
	Port      string    `json:"port"`
	StartedAt time.Time `json:"started_at"`
	Status    string    `json:"status"`
}

type ClusterState struct {
	Nodes           []string            `json:"nodes"`
	Replicas        map[string]*Replica `json:"replicas"`
	DesiredReplicas int                 `json:"desired_replicas"`
	Config          ServiceConfig       `json:"-"`
	mu              sync.RWMutex
}

var state = ClusterState{
	Nodes:           []string{}, // Сюда нужно добавить адреса агентов
	Replicas:        make(map[string]*Replica),
	DesiredReplicas: 0,
	Config: ServiceConfig{
		BinaryName: "worker", // Имя файла worker.exe или worker
		Args:       []string{},
	},
}

// Базовая проверка S3
func loadConfigFromS3() {
	log.Println("Config loaded from mock S3")
}

// Главный цикл мониторинга и реконсиляции
func orchestrationLoop() {
	ticker := time.NewTicker(3 * time.Second)
	for range ticker.C {
		reconcile()
		checkHealth()
	}
}

func checkHealth() {
	state.mu.Lock()
	defer state.mu.Unlock()

	// Проверяем доступность Нод
	activeNodes := []string{}
	for _, node := range state.Nodes {
		resp, err := http.Get(node + "/status")
		if err == nil && resp.StatusCode == 200 {
			activeNodes = append(activeNodes, node)
			resp.Body.Close()
		} else {
			log.Printf("Node %s is down!", node)
			// Если нода упала, помечаем все реплики на ней как Failed
			for id, replica := range state.Replicas {
				if replica.NodeURL == node {
					delete(state.Replicas, id) // Удаляем из стейта, реконсилер перезапустит на другой
					log.Printf("Replica %s lost due to node failure", id)
				}
			}
		}
	}
}

func reconcile() {
	state.mu.Lock()
	defer state.mu.Unlock()

	currentCount := len(state.Replicas)
	diff := state.DesiredReplicas - currentCount

	if diff > 0 {
		log.Printf("Scaling UP: need %d more replicas", diff)
		for i := 0; i < diff; i++ {
			startReplica()
		}
	} else if diff < 0 {
		log.Printf("Scaling DOWN: removing %d replicas", -diff)
		for i := 0; i < -diff; i++ {
			stopReplica()
		}
	}
}

func startReplica() {
	// Выбираем ноду
	if len(state.Nodes) == 0 {
		log.Println("No nodes available to start replica!")
		return
	}
	node := state.Nodes[rand.Intn(len(state.Nodes))]

	// Генерируем ID и Порт
	id := uuid.New().String()
	port := fmt.Sprintf("%d", 8000+rand.Intn(1000))

	// Запрос к Агенту
	reqBody := map[string]interface{}{
		"id":     id,
		"binary": state.Config.BinaryName,
		"port":   port,
		"args":   state.Config.Args,
	}
	jsonBody, _ := json.Marshal(reqBody)

	resp, err := http.Post(node+"/start", "application/json", bytes.NewBuffer(jsonBody))
	if err != nil || resp.StatusCode != 200 {
		log.Printf("Failed to start replica on %s: %v", node, err)
		return
	}

	state.Replicas[id] = &Replica{
		ID:        id,
		NodeURL:   node,
		Port:      port,
		StartedAt: time.Now(),
		Status:    "Running",
	}
	log.Printf("Replica %s started on %s:%s", id, node, port)
}

func stopReplica() {
	// Выбираем рандомно
	for id, replica := range state.Replicas {
		// Запрос к агенту
		client := &http.Client{}
		req, _ := http.NewRequest("GET", replica.NodeURL+"/stop?id="+id, nil)
		_, err := client.Do(req)

		if err != nil {
			log.Printf("Error stopping replica %s: %v", id, err)
		}

		delete(state.Replicas, id)
		log.Printf("Replica %s stopped", id)
		break // Удаляем по одной за цикл
	}
}

// Handler для управления масштабированием
func scaleHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Count int `json:"count"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	state.mu.Lock()
	state.DesiredReplicas = req.Count
	state.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Scaling to %d replicas", req.Count)
}

// Добавление ноды
func addNodeHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Url string `json:"url"` // http://ip:port
	}
	json.NewDecoder(r.Body).Decode(&req)

	state.mu.Lock()
	// Проверка на дубликаты
	exists := false
	for _, n := range state.Nodes {
		if n == req.Url {
			exists = true
		}
	}
	if !exists {
		state.Nodes = append(state.Nodes, req.Url)
	}
	state.mu.Unlock()

	log.Printf("Node added: %s", req.Url)
	w.WriteHeader(http.StatusOK)
}

// Карта состояния кластера
func statusHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.RLock()
	defer state.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// Proxy  для доступа к сервисам
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	state.mu.RLock()
	// выбираем случайную реплику
	replicas := make([]*Replica, 0, len(state.Replicas))
	for _, v := range state.Replicas {
		replicas = append(replicas, v)
	}
	state.mu.RUnlock()

	if len(replicas) == 0 {
		http.Error(w, "No available replicas", http.StatusServiceUnavailable)
		return
	}

	target := replicas[rand.Intn(len(replicas))]

	// Парсим URL ноды и меняем порт
	nodeUrl, _ := url.Parse(target.NodeURL)
	targetUrlString := fmt.Sprintf("%s://%s:%s", nodeUrl.Scheme, nodeUrl.Hostname(), target.Port)
	targetUrl, _ := url.Parse(targetUrlString)

	proxy := httputil.NewSingleHostReverseProxy(targetUrl)

	log.Printf("Proxying request to %s", targetUrlString)
	proxy.ServeHTTP(w, r)
}

func main() {
	loadConfigFromS3()

	// Запуск цикла оркестрации
	go orchestrationLoop()

	http.HandleFunc("/scale", scaleHandler)
	http.HandleFunc("/add-node", addNodeHandler) // "http://192.168.1.5:9090"
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/", proxyHandler)

	log.Println("Controller started on :8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}
