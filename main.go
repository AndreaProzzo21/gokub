package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Helpers per leggere le variabili d'ambiente in modo sicuro
func getEnvAsInt(key string, defaultVal int) int {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
		log.Printf("[WARN] Variabile %s errata, uso default: %d", key, defaultVal)
	}
	return defaultVal
}

func getEnvAsDuration(key string, defaultSecs int) time.Duration {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return time.Duration(val) * time.Second
		}
		log.Printf("[WARN] Variabile %s errata, uso default: %ds", key, defaultSecs)
	}
	return time.Duration(defaultSecs) * time.Second
}

func getEnvAsBool(key string, defaultVal bool) bool {
	if valStr := os.Getenv(key); valStr != "" {
		val, err := strconv.ParseBool(valStr)
		if err == nil {
			return val
		}
	}
	return defaultVal
}

func main() {
	// Setup log con data e ora
	log.SetFlags(log.Ldate | log.Ltime)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatal("ERRORE CRITICO: Variabile NODE_NAME non impostata.")
	}

	// Configurazione da ambiente
	tempThresholdDelta := getEnvAsInt("TEMP_THRESHOLD", 2)
	diskThresholdDelta := getEnvAsInt("DISK_THRESHOLD", 1)
	pollingInterval := getEnvAsDuration("POLL_INTERVAL_SEC", 15)
	debugMode := getEnvAsBool("DEBUG", false)

	// Inizializzazione Client K8s In-Cluster
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("ERRORE CRITICO lettura in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("ERRORE CRITICO creazione clientset: %v", err)
	}

	// Gestione Graceful Shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Ricevuto segnale di terminazione. Spegnimento agente...")
		cancel()
	}()

	log.Printf("Agente Edge avviato su: %s (Poll: %v, Debug: %t, Soglie: Temp=%d°C, Disk=%dGB)", 
		nodeName, pollingInterval, debugMode, tempThresholdDelta, diskThresholdDelta)

	var lastTemp, lastDisk int
	var lastWasError bool

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Agente arrestato con successo.")
			return
		case <-ticker.C:
			currentTemp := getCPUTemp()
			currentDisk := getFreeDiskSpace()

			// Delta-Patching: Verifica se la variazione supera le soglie
			if abs(currentTemp-lastTemp) >= tempThresholdDelta || abs(currentDisk-lastDisk) >= diskThresholdDelta {
				
				// Costruzione JSON Patch
				patchData := `{"metadata":{"annotations":{"edge.unibo.it/cpu-temp":"` + strconv.Itoa(currentTemp) + `","edge.unibo.it/disk-free-gb":"` + strconv.Itoa(currentDisk) + `"}` + `}}`
				
				_, err := clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, []byte(patchData), metav1.PatchOptions{})
				
				if err != nil {
					// Log spam prevention
					if !lastWasError {
						log.Printf("[ERRORE] Patch fallito su %s (sopprimo log identici successivi): %v", nodeName, err)
						lastWasError = true
					}
				} else {
					if lastWasError {
						log.Println("[INFO] Connessione all'API Server ripristinata con successo.")
						lastWasError = false
					}
					
					if debugMode {
						log.Printf("[DEBUG] API aggiornata - Temp: %d°C, Disk: %dGB", currentTemp, currentDisk)
					}
					
					// Aggiorniamo lo stato locale solo se la patch è andata a buon fine
					lastTemp = currentTemp
					lastDisk = currentDisk
				}
			}
		}
	}
}

// Lettura Temperatura (Fault-Tolerant per VM)
func getCPUTemp() int {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 35 
	}
	tempStr := strings.TrimSpace(string(data))
	tempMilli, err := strconv.Atoi(tempStr)
	if err != nil {
		return 35 
	}
	return tempMilli / 1000
}

// Lettura Disco Libero su /
func getFreeDiskSpace() int {
	var stat syscall.Statfs_t
	err := syscall.Statfs("/", &stat)
	if err != nil {
		return 0
	}
	freeBytes := stat.Bavail * uint64(stat.Bsize)
	return int(freeBytes / (1024 * 1024 * 1024))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
