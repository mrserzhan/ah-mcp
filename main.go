package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"
	"github.com/mrserzhan/ah-mcp/tools"
)

// version is set at build time via -ldflags="-X main.version=v1.2.3"
var version = "dev"

// appieVersion returns the version of github.com/gwillem/appie-go from build info.
func appieVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/gwillem/appie-go" {
			return dep.Version
		}
	}
	return "unknown"
}

const (
	defaultCallbackHost = "http://localhost:9876"
	defaultCallbackPort = 9876
	defaultMCPPort      = 3000
)

func main() {
	transport := flag.String("transport", "sse", "Transport mode: 'sse', 'streamable-http', or 'stdio'")
	remote := flag.Bool("remote", false, "Remote mode: disable auto browser-open on login (overridden by AH_REMOTE=true)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("ah-mcp %s (appie-go %s)\n", version, appieVersion())
		os.Exit(0)
	}

	// Init structured logger (always stderr + optional file via AH_LOG_FILE).
	tools.InitLogger(os.Getenv("AH_LOG_FILE"))

	// AH_REMOTE env var also enables remote mode.
	if os.Getenv("AH_REMOTE") == "true" {
		*remote = true
	}

	// Resolve config from environment.
	callbackHost := envOr("AH_CALLBACK_HOST", defaultCallbackHost)
	callbackPort := envIntOr("AH_CALLBACK_PORT", defaultCallbackPort)
	mcpPort := envIntOr("AH_MCP_PORT", defaultMCPPort)
	tokensPath := TokensPath()
	tools.LogInfo("startup", "config site=%s transport=%s remote=%t callback_host=%s callback_port=%d mcp_port=%d log_file_set=%t", ahSite(), *transport, *remote, callbackHost, callbackPort, mcpPort, os.Getenv("AH_LOG_FILE") != "")

	// Ensure token directory exists with secure permissions.
	if err := ensureTokenDir(tokensPath); err != nil {
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Warning: could not create token directory: %v\n", err)
	}

	// Build MCP server.
	s := server.NewMCPServer(
		"Albert Heijn",
		version,
		server.WithLogging(),
	)

	// Build dependency bundle.
	deps := tools.Deps{
		TokensPath:    tokensPath,
		CallbackHost:  callbackHost,
		CallbackPort:  callbackPort,
		RemoteMode:    *remote,
		ServerVersion: version,
		AppieVersion:  appieVersion(),
		GetClient:    GetClient,
		ReloadClient: ReloadClient,
		IsAuthenticated: func() bool {
			return IsAuthenticated(tokensPath)
		},
		StartOAuthFlow: func(ctx context.Context) (string, <-chan error, error) {
			return StartOAuthFlow(ctx, callbackHost, callbackPort, tokensPath)
		},
		RefreshIfNeeded: func(ctx context.Context) error {
			return RefreshIfNeeded(ctx, tokensPath)
		},
		Server: s,
	}

	// Register all tools.
	tools.RegisterLoginTool(s, deps)
	tools.RegisterProductTools(s, deps)
	tools.RegisterOrderTools(s, deps)
	tools.RegisterBasketTools(s, deps)
	tools.RegisterMemberTools(s, deps)
	tools.RegisterInfoTool(s, deps)

	ctx := context.Background()
	appieVer := appieVersion()

	switch *transport {
	case "stdio":
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Starting in stdio mode (version: %s, appie-go: %s)\n", version, appieVer)
		stdioSrv := server.NewStdioServer(s)
		if err := stdioSrv.Listen(ctx, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] stdio server error: %v\n", err)
			os.Exit(1)
		}
	case "sse", "":
		addr := fmt.Sprintf(":%d", mcpPort)
		baseURL := envOr("AH_MCP_BASE_URL", fmt.Sprintf("http://localhost:%d", mcpPort))
		mcpToken := os.Getenv("AH_MCP_TOKEN")
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Starting SSE server on %s (version: %s, appie-go: %s, base URL: %s, auth: %v)\n",
			addr, version, appieVer, baseURL, mcpToken != "")
		sseSrv := server.NewSSEServer(s, server.WithBaseURL(baseURL), server.WithKeepAlive(true), server.WithKeepAliveInterval(5*time.Second))
		var handler http.Handler = sseSrv
		if mcpToken != "" {
			handler = tokenAuthMiddleware(mcpToken, sseSrv)
		}
		httpServer := &http.Server{Addr: addr, Handler: handler}
		if err := httpServer.ListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] SSE server error: %v\n", err)
			os.Exit(1)
		}
	case "streamable-http":
		addr := fmt.Sprintf(":%d", mcpPort)
		baseURL := envOr("AH_MCP_BASE_URL", fmt.Sprintf("http://localhost:%d", mcpPort))
		mcpToken := os.Getenv("AH_MCP_TOKEN")
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Starting Streamable HTTP server on %s (version: %s, appie-go: %s, base URL: %s, auth: %v)\n",
			addr, version, appieVer, baseURL, mcpToken != "")
		httpSrv := server.NewStreamableHTTPServer(s,
			server.WithEndpointPath("/mcp"),
			server.WithHeartbeatInterval(5*time.Second),
		)
		var handler http.Handler = httpSrv
		if mcpToken != "" {
			handler = simpleAuthMiddleware(mcpToken, httpSrv)
		}
		httpServer := &http.Server{Addr: addr, Handler: handler}
		if err := httpServer.ListenAndServe(); err != nil {
			fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Streamable HTTP server error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "[Albert Heijn MCP] Unknown transport %q. Use 'sse', 'streamable-http', or 'stdio'.\n", *transport)
		os.Exit(1)
	}
}

// envOr returns the value of the named environment variable or the default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envIntOr returns the integer value of the named environment variable or the default.
func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// ensureTokenDir creates the directory for the tokens file with mode 0700.
func ensureTokenDir(tokensPath string) error {
	return os.MkdirAll(parentDir(tokensPath), 0700)
}

// tokenAuthMiddleware rejects requests that do not carry the expected token.
// The token is accepted as:
//   - Authorization: Bearer <token>  header, OR
//   - ?token=<token>                 query parameter
//
// Once an SSE connection is authenticated, the sessionId it receives is
// whitelisted so that subsequent /message posts (which don't carry the token)
// are also allowed.
func tokenAuthMiddleware(token string, next http.Handler) http.Handler {
	var sessions sync.Map // sessionId string -> struct{}

	isAuthed := func(r *http.Request) bool {
		if r.Header.Get("Authorization") == "Bearer "+token {
			return true
		}
		if r.URL.Query().Get("token") == token {
			return true
		}
		return false
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /message: allow if sessionId was established by an authenticated SSE connection.
		if strings.HasPrefix(r.URL.Path, "/message") {
			if sid := r.URL.Query().Get("sessionId"); sid != "" {
				if _, ok := sessions.Load(sid); ok {
					next.ServeHTTP(w, r)
					return
				}
			}
		}

		if !isAuthed(r) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// For SSE connections, wrap the ResponseWriter to capture the sessionId
		// from the "event: endpoint" SSE message and whitelist it.
		if strings.HasPrefix(r.URL.Path, "/sse") {
			next.ServeHTTP(&sessionCapture{ResponseWriter: w, sessions: &sessions}, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// sessionCapture wraps ResponseWriter to intercept SSE writes and extract
// the sessionId advertised in the "event: endpoint" message.
type sessionCapture struct {
	http.ResponseWriter
	sessions *sync.Map
}

func (sc *sessionCapture) Write(b []byte) (int, error) {
	if idx := bytes.Index(b, []byte("sessionId=")); idx >= 0 {
		rest := string(b[idx+len("sessionId="):])
		if end := strings.IndexAny(rest, "& \n\r"); end != -1 {
			rest = rest[:end]
		}
		if rest != "" {
			sc.sessions.Store(rest, struct{}{})
		}
	}
	return sc.ResponseWriter.Write(b)
}

func (sc *sessionCapture) Flush() {
	if f, ok := sc.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// simpleAuthMiddleware checks every request for a bearer token or ?token= query param.
// Used for streamable-http where each request is independent (no session tracking needed).
func simpleAuthMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+token {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Query().Get("token") == token {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}
