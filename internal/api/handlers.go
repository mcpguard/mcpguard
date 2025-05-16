package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/mcpguard/mcpguard/internal/config"
	"github.com/mcpguard/mcpguard/internal/detection"
	"github.com/mcpguard/mcpguard/internal/jsonrpc"
	"github.com/mcpguard/mcpguard/internal/mcp"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Session represents an active SSE connection
type Session struct {
	writer    http.ResponseWriter
	flusher   http.Flusher
	done      chan struct{}
	user      string
	targetUrl string
}

type API struct {
	config          *config.Config
	sessions        sync.Map
	detectionEngine *detection.Engine
}

func NewAgentAPI(config *config.Config, detectionEngine *detection.Engine) *API {
	return &API{
		config:          config,
		detectionEngine: detectionEngine,
	}
}

func (api *API) SSE(w http.ResponseWriter, r *http.Request) {
	// Parse the query parameters directly using the url package
	queryParams, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Error parsing query parameters", http.StatusBadRequest)
		return
	}

	targetUrl := ""

	for k, _ := range queryParams {
		if strings.Contains(k, "targetUrl") {
			// remove targetUrl = from k
			targetUrl = strings.TrimPrefix(k, "targetUrl=")
			break
		}
	}

	// replace http://localhost with http://host.docker.internal in targetUrl.
	if api.config.Docker {
		targetUrl = strings.ReplaceAll(targetUrl, "http://localhost", "http://host.docker.internal")
	} else {
	}
	// Use the extracted targetUrl as proxyURL
	//proxyURL := targetUrl + "/sse"
	// Create proxy request
	proxyReq, err := http.NewRequestWithContext(
		r.Context(),
		"GET",
		targetUrl,
		nil,
	)
	if err != nil {
		http.Error(w, "Error creating proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers and query parameters
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}
	proxyReq.URL.RawQuery = r.URL.RawQuery

	// Configure a client that won't time out
	transport := &http.Transport{
		DisableCompression: true,
		IdleConnTimeout:    0, // No timeout
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   0, // No timeout
	}

	// Send the request
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Error forwarding request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Ensure SSE headers are set
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Transfer-Encoding", "chunked")

	// Set response status
	w.WriteHeader(resp.StatusCode)

	// Immediately flush headers to client
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	flusher.Flush()

	// Store session information for this connection
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		// If no sessionId provided, extract it from the endpoint event
		// This will happen below when we process events
	}

	// Create a session object to track this connection
	// extract host name from targetUrl
	u, err := url.Parse(targetUrl)
	if err != nil {
		http.Error(w, "Error parsing target URL", http.StatusBadRequest)
		return
	}
	session := &Session{
		writer:    w,
		flusher:   flusher,
		done:      make(chan struct{}),
		targetUrl: u.Scheme + "://" + u.Host,
	}

	// Use a scanner to read lines from the response
	scanner := bufio.NewScanner(resp.Body)
	var collectingEvent strings.Builder
	var isEndpointEvent bool

	for scanner.Scan() {
		line := scanner.Text()

		// Detect event type
		if strings.HasPrefix(line, "event: endpoint") {
			isEndpointEvent = true
		}

		// Detect data line in endpoint event
		if isEndpointEvent && strings.HasPrefix(line, "data: ") {
			// Extract sessionId from original URL
			originalUrl := strings.TrimPrefix(line, "data: ")
			u, _ := url.Parse(originalUrl)
			sessionID = u.Query().Get("sessionId")

			// Store the session with the sessionID
			if sessionID != "" {
				api.sessions.Store(sessionID, session)
				// Setup cleanup when the connection is closed
				defer api.sessions.Delete(sessionID)
				defer close(session.done)
			}

			// Replace with our proxy URL
			newUrl := fmt.Sprintf("http://localhost:%d/message?sessionId=%s", api.config.ServerPort, sessionID)
			line = "data: " + newUrl
		}

		collectingEvent.WriteString(line + "\n")

		// Empty line indicates end of event
		if line == "" {
			// Write full event and reset
			fmt.Fprint(w, collectingEvent.String())
			flusher.Flush()

			collectingEvent.Reset()
			isEndpointEvent = false
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Error reading from target: %v\n", err)
	}
}

func (api *API) HandleMessage(w http.ResponseWriter, r *http.Request) {
	// Extract session ID
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, "Missing sessionId parameter", http.StatusBadRequest)
		return
	}

	session, ok := api.sessions.Load(sessionID)
	if !ok {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	sessionObj := session.(*Session)
	if sessionObj == nil {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// Set the target URL for the proxy
	targetUrl := sessionObj.targetUrl

	//targetUrl := "http://host.docker.internal:8084"
	proxyURL := targetUrl + "/message?sessionId=" + sessionID

	// Read the original request body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// deserialize bodyBytes to CallToolRequest
	var mcpRequest mcp.Request
	err = json.Unmarshal(bodyBytes, &mcpRequest)
	if err != nil {
		http.Error(w, "Error unmarshalling request body", http.StatusBadRequest)
		return
	}

	if mcpRequest.Method == "tools/call" {
		results := api.detectionEngine.Detect(mcpRequest)
		if len(results) > 0 {
			api.returnRpcError(sessionID, mcpRequest, results)
			return
		}
	}

	proxyReq, err := http.NewRequestWithContext(
		r.Context(),
		"POST",
		proxyURL,
		bytes.NewBuffer(bodyBytes),
	)
	if err != nil {
		http.Error(w, "Error creating proxy request", http.StatusInternalServerError)
		return
	}

	// Copy headers
	for key, values := range r.Header {
		for _, value := range values {
			proxyReq.Header.Add(key, value)
		}
	}

	// Send the request
	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Error forwarding request", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Read the response body
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "Error reading response body", http.StatusInternalServerError)
		return
	}

	// Copy the response status code
	w.WriteHeader(resp.StatusCode)

	// Copy the response headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	// Also send the response in the HTTP response
	w.Write(respBytes)
}

func (api *API) returnRpcError(sessionID string, request mcp.Request, results []detection.Result) {
	// format the results as a single string
	resultString := "Detected: "
	for _, result := range results {
		resultString += fmt.Sprintf("%s\n", result.Description)
	}

	sessionObj, exists := api.sessions.Load(sessionID)
	if exists {
		session := sessionObj.(*Session)

		// Format as SSE event
		b, _ := json.Marshal(jsonrpc.Error{
			JSONRPC: "2.0",
			ID:      request.ID,
			Error: struct {
				Code    int         `json:"code"`
				Message string      `json:"message"`
				Data    interface{} `json:"data,omitempty"`
			}{
				Code:    -32603,
				Message: "Blocked by MCPGuard. This has been blocked because it contains sensitive information. Details: " + resultString,
				Data:    nil,
			},
		})
		event := fmt.Sprintf("event: message\ndata: %s\n\n", b)

		// Write to SSE connection
		select {
		case <-session.done:
			// Session is closed
		default:
			fmt.Fprint(session.writer, event)
			session.flusher.Flush()
		}
	}
}
