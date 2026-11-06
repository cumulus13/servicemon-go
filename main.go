// File: ..\servicemon-go\main.go
// Author: Hadi Cahyadi <cumulus13@gmail.com>
// Date: 2026-11-06
// Description: 
// License: MIT

package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ServiceRule represents a monitored Windows service and its expected state
type ServiceRule struct {
	Name           string
	ExpectedStatus string
}

// Config holds all configuration loaded from servicemon.ini
type Config struct {
	SleepTime  float64
	GeneralLog string
	ErrorLog   string
	RawNames   string
}

const (
	GNTPApp      = "Servicemon"
	GNTPNotif    = "Service Stop"
	DefaultHost  = "127.0.0.1"
	DefaultPort  = "23053"
)

func main() {
	// Command line flags for configuration management
	initFlag := flag.Bool("init", false, "Initialize a default servicemon.ini configuration file")
	pathFlag := flag.Bool("path", false, "Show the absolute path of servicemon.ini")
	showFlag := flag.Bool("show", false, "Show current configuration settings and loaded rules")
	debugPtr := flag.Bool("debug", false, "Enable debug mode")
	flag.Parse()

	// Resolve base directory
	baseDir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		baseDir = "."
	}

	iniPath := filepath.Join(baseDir, "servicemon.ini")
	iconPath := filepath.Join(baseDir, "service.png")

	// Handle --init flag
	if *initFlag {
		createDefaultConfig(iniPath)
		return
	}

	// Handle --path flag
	if *pathFlag {
		fmt.Println(iniPath)
		return
	}

	// Determine debug status
	debug := *debugPtr || os.Getenv("DEBUG") == "1" || os.Getenv("DEBUG") == "true"
	if debug {
		log.Println("🐞 Debug mode enabled [ServiceMon-Go]")
	}

	// Load configuration using our robust custom INI parser
	cfg := parseINI(iniPath, baseDir)
	sleepDuration := time.Duration(cfg.SleepTime * float64(time.Second))

	// Parse service names and expected statuses
	var rules []ServiceRule
	for _, line := range strings.Split(cfg.RawNames, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		for _, field := range strings.Fields(line) {
			field = strings.TrimSpace(field)
			if field == "" || strings.HasPrefix(field, "#") {
				continue
			}
			parts := strings.SplitN(field, "#", 2)
			if len(parts) == 2 {
				rules = append(rules, ServiceRule{
					Name:           strings.TrimSpace(parts[0]),
					ExpectedStatus: strings.TrimSpace(parts[1]),
				})
			}
		}
	}

	// GNTP Configuration from environment or defaults
	gntpHost := getEnv("GNTP_HOST", DefaultHost)
	gntpPort := getEnv("GNTP_PORT", DefaultPort)

	// Register with GNTP server at startup (matching Python GrowlNotifier.register())
	registerGNTP(gntpHost, gntpPort, GNTPApp, GNTPNotif, debug)

	// Handle --show flag
	if *showFlag {
		fmt.Printf("Configuration Path : %s\n", iniPath)
		fmt.Printf("Sleep Interval     : %v\n", sleepDuration)
		fmt.Printf("General Log        : %s\n", cfg.GeneralLog)
		fmt.Printf("Error Log          : %s\n", cfg.ErrorLog)
		fmt.Printf("GNTP Endpoint      : %s:%s\n", gntpHost, gntpPort)
		fmt.Printf("Loaded Rules (%d)   :\n", len(rules))
		for i, r := range rules {
			fmt.Printf("  %d. Service: %s | Expected Status: %s\n", i+1, r.Name, r.ExpectedStatus)
		}
		return
	}

	if debug {
		log.Printf("Loaded %d service rules. Sleep interval: %v", len(rules), sleepDuration)
	}

	// Main monitoring daemon loop
	for {
		for _, rule := range rules {
			actualStatus, err := getServiceStatus(rule.Name)
			if err != nil {
				if debug {
					log.Printf("Error querying service %s: %v", rule.Name, err)
				}
				continue
			}

			if debug {
				log.Printf("Service: %s | Expected: %s | Actual: %s", rule.Name, rule.ExpectedStatus, actualStatus)
			}

			// Check if the service is in the expected state (case-insensitive)
			if strings.Contains(strings.ToLower(actualStatus), strings.ToLower(rule.ExpectedStatus)) {
				continue
			}

			// Service is not in expected state -> Trigger stop and GNTP notification
			fmt.Printf("[%s] - stop service '%s'\n", time.Now().Format("2006/01/02 15:04:05.000"), rule.Name)

			sendGNTPNotification(gntpHost, gntpPort, GNTPApp, GNTPNotif, rule.Name, iconPath, debug)
			stopService(rule.Name, cfg.GeneralLog, cfg.ErrorLog)
		}

		time.Sleep(sleepDuration)
	}
}

// registerGNTP sends the GNTP REGISTER packet to the server at startup
func registerGNTP(host, port, appName, notifName string, debug bool) {
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		if debug {
			log.Printf("GNTP registration connection failed: %v", err)
		}
		return
	}
	defer conn.Close()

	packet := fmt.Sprintf(
		"GNTP/1.0 REGISTER NONE\r\n"+
			"Application-Name: %s\r\n"+
			"Notifications-Count: 1\r\n\r\n"+
			"Notification-Name: %s\r\n"+
			"Notification-Display-Name: %s\r\n"+
			"Notification-Enabled: True\r\n\r\n",
		appName, notifName, notifName,
	)

	_, err = conn.Write([]byte(packet))
	if err != nil && debug {
		log.Printf("Failed to send GNTP register packet: %v", err)
	} else if debug {
		log.Printf("GNTP registered successfully at %s", addr)
	}
}

// sendGNTPNotification sends a fully compliant GNTP NOTIFY packet when stopping a service
func sendGNTPNotification(host, port, appName, notifName, serviceName, iconPath string, debug bool) {
	addr := net.JoinHostPort(host, port)
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		if debug {
			log.Printf("GNTP notification connection failed: %v", err)
		}
		return
	}
	defer conn.Close()

	packet := fmt.Sprintf(
		"GNTP/1.0 NOTIFY NONE\r\n"+
			"Application-Name: %s\r\n"+
			"Notification-Name: %s\r\n"+
			"Notification-Title: ServiceMon\r\n"+
			"Notification-Text: Stopping service %s\r\n"+
			"Notification-Icon: %s\r\n\r\n",
		appName, notifName, serviceName, iconPath,
	)

	_, err = conn.Write([]byte(packet))
	if err != nil && debug {
		log.Printf("GNTP notification failed: %v", err)
	}
}

// parseINI reads and parses servicemon.ini safely without treating '#' as inline comments
func parseINI(path string, baseDir string) Config {
	cfg := Config{
		SleepTime:  5.0,
		GeneralLog: filepath.Join(baseDir, "servicemon_general.log"),
		ErrorLog:   filepath.Join(baseDir, "servicemon_error.log"),
		RawNames:   "",
	}

	file, err := os.Open(path)
	if err != nil {
		return cfg
	}
	defer file.Close()

	currentSection := ""
	scanner := bufio.NewScanner(file)
	var currentKey string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			currentKey = ""
			continue
		}

		if idx := strings.Index(line, "="); idx != -1 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			currentKey = strings.ToLower(key)

			switch currentSection {
			case "service":
				if currentKey == "names" {
					cfg.RawNames = val
				}
			case "time":
				if currentKey == "sleep" {
					var f float64
					_, _ = fmt.Sscanf(val, "%f", &f)
					if f > 0 {
						cfg.SleepTime = f
					}
				}
			case "log":
				if currentKey == "general" {
					cfg.GeneralLog = val
					if !filepath.IsAbs(cfg.GeneralLog) {
						cfg.GeneralLog = filepath.Join(baseDir, cfg.GeneralLog)
					}
				} else if currentKey == "error" {
					cfg.ErrorLog = val
					if !filepath.IsAbs(cfg.ErrorLog) {
						cfg.ErrorLog = filepath.Join(baseDir, cfg.ErrorLog)
					}
				}
			}
		} else if currentKey != "" && currentSection == "service" {
			cfg.RawNames += "\n" + line
		}
	}

	return cfg
}

// createDefaultConfig generates a default servicemon.ini file if it doesn't exist
func createDefaultConfig(path string) {
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("Configuration file already exists at: %s\n", path)
		return
	}

	defaultContent := `; ServiceMon Configuration File

[service]
names = wuauserv#stop clicktorunsvc#stop

[time]
sleep = 5

[log]
general = servicemon_general.log
error = servicemon_error.log
`

	err := os.WriteFile(path, []byte(defaultContent), 0644)
	if err != nil {
		log.Fatalf("Failed to create default configuration file: %v", err)
	}

	fmt.Printf("Successfully created default configuration file at: %s\n", path)
}

// getEnv retrieves environment variable with a fallback default value
func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

// getServiceStatus queries the Windows service status using the native 'sc query' command
func getServiceStatus(serviceName string) (string, error) {
	cmd := exec.Command("sc", "query", serviceName)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "STATE") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				fields := strings.Fields(parts[1])
				if len(fields) > 0 {
					return fields[len(fields)-1], nil
				}
			}
		}
	}
	return "UNKNOWN", nil
}

// stopService executes 'sc stop' on the target service and appends output to log files
func stopService(serviceName, generalLogPath, errorLogPath string) {
	cmd := exec.Command("sc", "stop", serviceName)

	genFile, err := os.OpenFile(generalLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		defer genFile.Close()
		cmd.Stdout = genFile
	}

	errFile, err := os.OpenFile(errorLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err == nil {
		defer errFile.Close()
		cmd.Stderr = errFile
	}

	_ = cmd.Run()
}