package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type Config struct {
	QbitDownloadsBaseDir string   `json:"qbit_downloads_basedir"`
	WatchDirs            []string `json:"watch_dirs"`
	CopyParams           string   `json:"copy_params"`
	RcloneRemoteName     string   `json:"rclone_remote_name"`
	RemoteStatus         []string `json:"remote_status"`
}

var (
	config             Config
	statsMutex         sync.Mutex
	statsData          string
	uploadedFiles      = make(map[string]bool)
	uploadedFilesMutex sync.Mutex
)

func main() {
	log.Println("Starting Rclone Uploader application")
	loadConfig()
	loadUploadedFiles()
	go watchDirectories()

	http.HandleFunc("/", handleIndex)
	http.HandleFunc("/update-config", handleUpdateConfig)
	http.HandleFunc("/stats", handleStats)

	log.Println("Server starting on :8050")
	log.Fatal(http.ListenAndServe(":8050", nil))
}

func loadConfig() {
	log.Println("Loading configuration from config.json")
	file, err := os.Open("config.json")
	if err != nil {
		log.Fatal("Error opening config file:", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Fatal("Error closing config file:", err)
		}
	}(file)

	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		log.Fatal("Error decoding config:", err)
	}

	log.Printf("Configuration loaded: %+v", config)
}

func loadUploadedFiles() {
	log.Println("Loading uploaded files from uploaded_files.txt")
	file, err := os.Open("uploaded_files.txt")
	if err != nil {
		if os.IsNotExist(err) {
			log.Println("uploaded_files.txt does not exist, starting fresh")
			return
		}
		log.Fatal("Error opening uploaded_files.txt:", err)
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Fatal("Error closing uploaded_files.txt:", err)
		}
	}(file)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		uploadedFiles[scanner.Text()] = true
	}

	if err := scanner.Err(); err != nil {
		log.Fatal("Error reading uploaded_files.txt:", err)
	}

	log.Println("Uploaded files loaded successfully")
}

func saveUploadedFile(path string) {
	file, err := os.OpenFile("uploaded_files.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Error opening uploaded_files.txt for writing: %v", err)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Printf("Error closing uploaded_files.txt: %v", err)
		}
	}(file)

	if _, err := file.WriteString(path + "\n"); err != nil {
		log.Printf("Error writing to uploaded_files.txt: %v", err)
	}
}

func watchDirectories() {
	log.Println("Setting up directory watcher")
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal("Error creating watcher:", err)
	}
	defer func(watcher *fsnotify.Watcher) {
		err := watcher.Close()
		if err != nil {
			log.Fatal("Error closing watcher:", err)
		}
	}(watcher)

	for _, dir := range config.WatchDirs {
		fullPath := filepath.Join(config.QbitDownloadsBaseDir, dir)
		log.Printf("Adding directory to watch: %s", fullPath)
		err = watcher.Add(fullPath)
		if err != nil {
			log.Printf("Error watching directory %s: %v", fullPath, err)
		}
	}

	log.Println("Directory watcher setup complete")

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == fsnotify.Create {
				log.Printf("New file detected: %s", event.Name)
				uploadFile(event.Name)
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Println("Error in watcher:", err)
		}
	}
}

func uploadFile(path string) {
	uploadedFilesMutex.Lock()
	if uploadedFiles[path] {
		log.Printf("File %s has already been uploaded, skipping.", path)
		uploadedFilesMutex.Unlock()
		return
	}
	uploadedFilesMutex.Unlock()

	log.Printf("Preparing to upload file: %s", path)
	relPath, err := filepath.Rel(config.QbitDownloadsBaseDir, path)
	if err != nil {
		log.Printf("Error getting relative path: %v", err)
		return
	}

	sourcePath := path
	destPath := filepath.Join(config.RcloneRemoteName+":", relPath)

	args := append([]string{"copy"}, splitParams(config.CopyParams)...)
	args = append(args, sourcePath, destPath)

	log.Printf("Executing rclone command: rclone %s", strings.Join(args, " "))

	cmd := exec.Command("rclone", args...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("Error creating stdout pipe: %v", err)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("Error starting rclone command: %v", err)
		return
	}

	go func() {
		reader := bufio.NewReader(stdoutPipe)
		var outputBuilder strings.Builder
		capturing := false

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Printf("Error reading stdout: %v", err)
				return
			}

			if strings.Contains(line, "Transferred:") {
				parts := strings.Split(line, "Transferred:")
				for i, part := range parts {
					if i > 0 {
						outputBuilder.WriteString("\nTransferred:")
					}
					outputBuilder.WriteString(part)
				}
			} else {
				outputBuilder.WriteString(line)
			}

			if strings.HasPrefix(line, "Transferring:") {
				capturing = true
			}

			if capturing {
				if strings.HasPrefix(line, "Transferred:") {
					capturing = false
					updateStats(outputBuilder.String())
					outputBuilder.Reset()
				} else {
					outputBuilder.WriteString(line)
				}
			}
		}
	}()
	if err := cmd.Wait(); err != nil {
		log.Printf("Error during rclone upload: %v", err)
		return
	}

	log.Printf("Successfully uploaded %s to %s", sourcePath, destPath)
	updateStats(fmt.Sprintf("Successfully uploaded %s to %s", sourcePath, destPath))

	uploadedFilesMutex.Lock()
	uploadedFiles[path] = true
	uploadedFilesMutex.Unlock()

	saveUploadedFile(path)
}

func splitParams(s string) []string {
	var params []string
	var current strings.Builder
	inQuotes := false

	for _, r := range s {
		switch {
		case r == '"' && !inQuotes:
			inQuotes = true
		case r == ' ' && !inQuotes:
			if current.Len() > 0 {
				params = append(params, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}

	if current.Len() > 0 {
		params = append(params, current.String())
	}

	return params
}

func updateStats(output string) {
	statsMutex.Lock()
	defer statsMutex.Unlock()
	statsData = output
}

func handleIndex(w http.ResponseWriter, _ *http.Request) {
	log.Println("Handling index page request")
	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		log.Printf("Error parsing index.html template: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = tmpl.Execute(w, config)
	if err != nil {
		log.Printf("Error executing index.html template: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	log.Println("Handling config update request")
	err := r.ParseForm()
	if err != nil {
		log.Printf("Error parsing form: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	config.QbitDownloadsBaseDir = r.FormValue("qbit_downloads_basedir")
	config.WatchDirs = strings.Split(r.FormValue("watch_dirs"), ",")
	config.CopyParams = r.FormValue("copy_params")
	config.RcloneRemoteName = r.FormValue("rclone_remote_name")

	log.Printf("Updated configuration: %+v", config)

	file, err := os.Create("config.json")
	if err != nil {
		log.Printf("Error creating config.json: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			log.Printf("Error closing config.json: %v", err)
		}
	}(file)

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(config)
	if err != nil {
		log.Printf("Error encoding config to JSON: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Println("Configuration updated successfully")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func handleStats(w http.ResponseWriter, _ *http.Request) {
	statsMutex.Lock()
	defer statsMutex.Unlock()
	_, fprint := fmt.Fprint(w, statsData)
	if fprint != nil {
		return
	}
}
