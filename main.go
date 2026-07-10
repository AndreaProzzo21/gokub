package main

import (
	"context"
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

func main() {
	log.SetFlags(log.Ldate | log.Ltime)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatal("ERRORE: NODE_NAME non impostata.")
	}

	tempThreshold := getEnvAsInt("TEMP_THRESHOLD", 2)
	diskThreshold := getEnvAsInt("DISK_THRESHOLD", 1)
	netThreshold := getEnvAsInt("NET_THRESHOLD", 10) // 10ms di tolleranza
	pollingInterval := getEnvAsDuration("POLL_INTERVAL_SEC", 15)
	debugMode := getEnvAsBool("DEBUG", false)

	config, err := rest.InClusterConfig()
	if err != nil { log.Fatalf("ERRORE config: %v", err) }
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil { log.Fatalf("ERRORE clientset: %v", err) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Spegnimento in corso...")
		cancel()
	}()

	log.Printf("Gokub avviato su: %s (Poll: %v, Debug: %t)", nodeName, pollingInterval, debugMode)

	var lastTemp, lastDisk, lastNet int
	var lastWasError bool

	ticker := time.NewTicker(pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			currentTemp := getCPUTemp()
			currentDisk := getFreeDiskSpace()
			currentNet := getNetworkLatencyMs()

			if abs(currentTemp-lastTemp) >= tempThreshold || 
			   abs(currentDisk-lastDisk) >= diskThreshold || 
			   abs(currentNet-lastNet) >= netThreshold {
				
				// Creazione corretta del JSON Patch con prefisso gokub.io
				patchData := `{"metadata":{"annotations":{` +
					`"gokub.io/cpu-temp":"` + strconv.Itoa(currentTemp) + `",` +
					`"gokub.io/disk-free-gb":"` + strconv.Itoa(currentDisk) + `",` +
					`"gokub.io/net-latency-ms":"` + strconv.Itoa(currentNet) + `"` +
					`}}}`
				
				_, err := clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, []byte(patchData), metav1.PatchOptions{})
				
				if err != nil {
					if !lastWasError {
						log.Printf("[ERRORE] Patch fallito su %s: %v", nodeName, err)
						lastWasError = true
					}
				} else {
					if lastWasError {
						log.Println("[INFO] API ripristinata.")
						lastWasError = false
					}
					if debugMode {
						log.Printf("[DEBUG] Patch OK - Temp: %d, Disk: %d, Net: %dms", currentTemp, currentDisk, currentNet)
					}
					lastTemp = currentTemp
					lastDisk = currentDisk
					lastNet = currentNet
				}
			}
		}
	}
}

// Lettura Temperatura
func getCPUTemp() int {
	data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil { return 35 } // Fallback se non c'è sensore
	t, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil { return 35 }
	return t / 1000
}

// Lettura Disco Libero (Ora punta alla cartella montata dall'host!)
func getFreeDiskSpace() int {
	var stat syscall.Statfs_t
	// Leggiamo /host-root che definireremo nello YAML
	if err := syscall.Statfs("/host-root", &stat); err != nil { return 0 }
	return int((stat.Bavail * uint64(stat.Bsize)) / (1024 * 1024 * 1024))
}

// NOVITÀ: Latenza Rete in Puro Go (TCP Ping verso DNS Google)
func getNetworkLatencyMs() int {
	start := time.Now()
	// Timeout di 2 secondi
	conn, err := net.DialTimeout("tcp", "8.8.8.8:53", 2*time.Second)
	if err != nil {
		return 999 // Segnaliamo 999ms in caso di disconnessione
	}
	conn.Close()
	return int(time.Since(start).Milliseconds())
}

func abs(x int) int {
	if x < 0 { return -x }
	return x
}
