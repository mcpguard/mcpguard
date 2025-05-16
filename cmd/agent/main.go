package main

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/mux"
	"github.com/mcpguard/mcpguard/internal/api"
	"github.com/mcpguard/mcpguard/internal/config"
	"github.com/mcpguard/mcpguard/internal/detection"
	"github.com/spf13/cobra"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func main() {
	var rootCmd = &cobra.Command{
		Use:   "mcpguard",
		Short: "MCP Guard - A secure gateway for MCP servers",
		Run:   runServer,
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func runServer(cmd *cobra.Command, args []string) {
	// Load configuration
	cfg := config.NewConfig()

	if err := transformMcpConfiguration(cfg); err != nil {
		log.Fatalf("Failed to transform MCP configuration: %v", err)
	}

	// Create router
	router := mux.NewRouter()

	// MCP routes
	d, err := detection.NewEngine()
	if err != nil {
		log.Fatalf("Failed to create detection engine: %v", err)
	}

	h := api.NewAgentAPI(cfg, d)

	router.HandleFunc("/sse", h.SSE).Methods("GET")
	router.HandleFunc("/message", h.HandleMessage).Methods("POST")

	// Create HTTP server
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.ServerPort),
		Handler: router,
	}

	// Channel to listen for errors coming from the listener
	serverErrors := make(chan error, 1)

	// Start the server
	go func() {
		log.Printf("Starting MCP Guard agent on port %d", cfg.ServerPort)
		serverErrors <- server.ListenAndServe()
	}()

	// Channel to listen for an interrupt or terminate signal from the OS
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Block until we receive a signal or error
	select {
	case err := <-serverErrors:
		log.Fatalf("Error starting server: %v", err)
	case <-shutdown:
		log.Println("Shutting down server...")
		// Gracefully shutdown the server
		server.Close()
		log.Println("Agent shut down successfully")
	}
}

func transformMcpConfiguration(cfg *config.Config) error {
	// Common paths where MCP configuration files might be located
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get user home directory: %w", err)
	}

	// Define common paths for MCP configuration files
	paths := []string{
		filepath.Join(userHome, "intellij", "mcp.json"),
		filepath.Join(".", ".cursor", "mcp.json"),
		filepath.Join(userHome, "Library", "Application Support", "Code", "User", "settings.json"),
		// Add more common paths as needed
	}

	// Keep track of successfully modified files
	var modifiedFiles []string

	for _, path := range paths {
		modified, err := transformMcpConfigFile(path)
		if err != nil {
			log.Printf("Warning: failed to transform MCP config at %s: %v", path, err)
			continue
		}
		if modified {
			modifiedFiles = append(modifiedFiles, path)
		}
	}

	if len(modifiedFiles) > 0 {
		log.Printf("Successfully modified %d MCP configuration files:", len(modifiedFiles))
		for _, file := range modifiedFiles {
			log.Printf("  - %s", file)
		}
	} else {
		log.Println("No MCP configuration files were found or modified")
	}

	return nil
}

// transformMcpConfigFile modifies a single MCP config file to redirect requests through the local proxy
func transformMcpConfigFile(filePath string) (bool, error) {
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		return false, nil // File doesn't exist, not an error
	}

	// Read the file
	data, err := os.ReadFile(filePath)
	if err != nil {
		return false, fmt.Errorf("failed to read file: %w", err)
	}

	// Parse JSON
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return false, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Check if we need to modify the file
	modified := false

	// Process the configuration to update URLs
	modified = updateURLsInConfig(config)

	if !modified {
		return false, nil // No changes needed
	}

	// Create backup of the original file
	backupPath := filePath + ".bak"
	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return false, fmt.Errorf("failed to create backup file: %w", err)
	}

	// Write the modified configuration
	updatedData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return false, fmt.Errorf("failed to marshal JSON: %w", err)
	}

	if err := os.WriteFile(filePath, updatedData, 0644); err != nil {
		return false, fmt.Errorf("failed to write modified file: %w", err)
	}

	return true, nil
}

// updateURLsInConfig recursively searches for URL fields in the config and updates them
func updateURLsInConfig(data interface{}) bool {
	modified := false

	switch v := data.(type) {
	case map[string]interface{}:
		// Check if this is a URL field
		if url, ok := v["url"].(string); ok {
			// Skip URLs that have already been modified
			if !strings.HasPrefix(url, "http://localhost:11435?targetUrl=") {
				v["url"] = "http://localhost:11435?targetUrl=" + url
				modified = true
			}
		}

		// Recursively process all fields
		for _, value := range v {
			if subModified := updateURLsInConfig(value); subModified {
				modified = true
			}
		}

	case []interface{}:
		// Process array elements
		for _, item := range v {
			if subModified := updateURLsInConfig(item); subModified {
				modified = true
			}
		}
	}

	return modified
}
