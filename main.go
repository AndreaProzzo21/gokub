package main

import (
	"context"
	"fmt"
	"log"
	"net"
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

// --- Funzioni Helper per le Variabili d'Ambiente ---

func getEnvAsInt(key string, defaultVal int) int {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return defaultVal
}

func getEnvAsDuration(key string, defaultSecs int) time.Duration {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return time.Duration(val) * time.Second
		}
	}
	return time.Duration(defaultSecs) * time.Second
}

func getEnvAsBool(key string, defaultVal bool) bool {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.ParseBool(valStr); err == nil {
			return val
		}
	}
	return defaultVal
}

// parseTargets trasforma la stringa "cloud=8.8.8.8:53,db=1.1.1.1:53" in una mappa Go
func parseTargets(raw string) map[string]string {
	targets := make(map[string]string)
	if raw == "" {
		return targets
	}
	pairs := strings.Split(raw, ",")
	for _, pair := range pairs {
		kv := strings.Split(pair, "=")
		if len(kv) == 2 {
			targets[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return targets
}

// --- Logica Principale ---

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatal("ERRORE: Variabile NODE_NAME non impostata.")
	}

	// Lettura Configurazioni
	tempThreshold := getEnvAsInt("TEMP_THRESHOLD", 2)
	diskThreshold := getEnvAsInt("DISK_THRESHOLD", 1)
	netThreshold := getEnvAsInt("NET_THRESHOLD", 10) // 10ms di tolleranza per i ping
	pollingInterval := getEnvAsDuration("POLL_INTERVAL_SEC", 15)
	debugMode := getEnvAsBool("DEBUG", false)
	
	// Lettura dei Target di Rete Dinamici
	pingTargetsRaw := os.Getenv("PING_TARGETS")
	targets := parseTargets(pingTargetsRaw)

	// Inizializzazione Client Kubernetes
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("ERRORE configurazione in-cluster: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("ERRORE creazione clientset: %v", err)
	}

	// Gestione Graceful Shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Ricevuto segnale di terminazione. Spegnimento in corso...")
		cancel()
	}()

	log.Printf("Gokub avviato su: %s (Poll: %v, Debug: %t)", nodeName, pollingInterval, debugMode)
	if len(targets) > 0 {
		log.Printf("Monitoraggio latenza verso i seguenti target: %v", targets)
	} else {
		log.Println("Nessun target di rete configurato (variabile PING_TARGETS vuota).")
	}

	// Variabili di stato per calcolare le soglie (Delta-Patching)
	var lastTemp, lastDisk int
	lastNet := make(map[string]int) // Mappa per tracciare la latenza precedente di ogni target
	var lastWasError bool
	firstRun := true // Forza l'aggiornamento al primo ciclo

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentTemp := getCPUTemp()
			currentDisk := getFreeDiskSpace()
			currentNet := make(map[string]int)
			shouldUpdate := false

			// 1. Controllo Soglie Hard-Ware
			if firstRun || abs(currentTemp-lastTemp) >= tempThreshold {
				shouldUpdate = true
			}
			if firstRun || abs(currentDisk-lastDisk) >= diskThreshold {
				shouldUpdate = true
			}

			// 2. Costruzione JSON Base (Temp e Disco)
			annotations := []string{
				fmt.Sprintf(`"gokub.io/cpu-temp":"%d"`, currentTemp),
				fmt.Sprintf(`"gokub.io/disk-free-gb":"%d"`, currentDisk),
			}

			// 3. Controllo Soglie di Rete Multi-Target e Costruzione JSON Rete
			for name, address := range targets {
				lat := getNetworkLatencyMs(address)
				currentNet[name] = lat
				
				if firstRun || abs(lat-lastNet[name]) >= netThreshold {
					shouldUpdate = true
				}
				
				annotations = append(annotations, fmt.Sprintf(`"gokub.io/net-latency-%s":"%d"`, name, lat))
			}

			// 4. Esecuzione Patch solo se i parametri hanno superato la soglia
			if shouldUpdate {
				// Assembla tutte le annotazioni in modo sicuro
				patchData := `{"metadata":{"annotations":{` + strings.Join(annotations, ",") + `}}}`

				_, err := clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, []byte(patchData), metav1.PatchOptions{})

				if err != nil {
					// Log Spam Prevention per gli errori
					if !lastWasError {
						log.Printf("[ERRORE] Patch fallito sul nodo %s: %v", nodeName, err)
						lastWasError = true
					}
				} else {
					if lastWasError {
						log.Println("[INFO] Connessione all'API ripristinata con successo.")
						lastWasError = false
					}
					if debugMode {
						log.Printf("[DEBUG] Patch applicata con successo. Temp: %d, Disk: %d, Net: %v", currentTemp, currentDisk, currentNet)
					}
					
					// Aggiorna lo stato locale per il prossimo ciclo
					lastTemp = currentTemp
					lastDisk = currentDisk
					for k, v := range currentNet {
						lastNet[k] = v
					}
					firstRun = false
				}
			}
		}
	}
}

// --- Funzioni di Raccolta Telemetria ---

// getCPUTemp legge il sensore termico. Ritorna 35°C (valore nominale) se il sensore non esiste (es. VM).
func getCPUTemp() int {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 35
	}
	t, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 35
	}
	return t / 1000
}

// getFreeDiskSpace calcola i Gigabyte liberi sulla partizione host-root.
func getFreeDiskSpace() int {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/host-root", &stat); err != nil {
		return 0
	}
	return int((stat.Bavail * uint64(stat.Bsize)) / (1024 * 1024 * 1024))
}

// getNetworkLatencyMs esegue un dial TCP misurando il Round-Trip Time (RTT).
func getNetworkLatencyMs(address string) int {
	start := time.Now()
	// Timeout rigoroso di 2 secondi per evitare stalli se il target è down
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return 999 // Ritorna 999ms per indicare una connessione assente o pessima
	}
	conn.Close()
	return int(time.Since(start).Milliseconds())
}

// abs calcola il valore assoluto per valutare i delta
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}