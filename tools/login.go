package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// openBrowser attempts to open url in the default system browser.
// Runs in a goroutine — failure is silently ignored.
func openBrowser(url string) {
	go func() {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		case "darwin":
			cmd = exec.Command("open", url)
		default: // linux and others
			cmd = exec.Command("xdg-open", url)
		}
		_ = cmd.Start()
	}()
}

// Deps holds the dependencies injected into every tool handler.
type Deps struct {
	// TokensPath is the path to the tokens.json file.
	TokensPath string
	// CallbackHost is the base URL for the OAuth callback server (e.g. http://localhost:9876).
	CallbackHost string
	// CallbackPort is the port the temporary OAuth proxy listens on.
	CallbackPort int
	// RemoteMode disables automatic browser opening during login.
	// Set this when the server runs on a machine without a display (remote/cloud).
	RemoteMode bool
	// GetClient returns the authenticated appie client.
	GetClient func() (*appie.Client, error)
	// ReloadClient recreates the client from the tokens file.
	ReloadClient func() (*appie.Client, error)
	// IsAuthenticated checks whether valid tokens are on disk.
	IsAuthenticated func() bool
	// StartOAuthFlow starts the proxy and returns (loginURL, doneChan, err).
	StartOAuthFlow func(ctx context.Context) (string, <-chan error, error)
	// RefreshIfNeeded refreshes the access token if it is close to expiry.
	RefreshIfNeeded func(ctx context.Context) error
	// Server is the MCP server instance (kept for future use).
	Server *server.MCPServer
	// ServerVersion is the build version of the MCP server binary.
	ServerVersion string
	// AppieVersion is the version of the appie-go library in use.
	AppieVersion string
}

func notAuthResult() *mcp.CallToolResult {
	return mcp.NewToolResultError(`{"error":"not_authenticated","message":"Not logged in. Call ah_login first."}`)
}

func errResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultError(msg)
}

// oauthFlow tracks an in-progress OAuth flow across tool calls.
// ah_login call 1 → starts the flow, returns the URL immediately.
// ah_login call 2 → checks whether the browser callback was received.
var oauthFlow struct {
	sync.Mutex
	active   bool
	loginURL string
	done     <-chan error
}

// RegisterLoginTool registers the ah_login and ah_logout MCP tools.
func RegisterLoginTool(s *server.MCPServer, deps Deps) {
	registerLogout(s, deps)
	tool := mcp.NewTool("ah_login",
		mcp.WithTitleAnnotation("Albert Heijn: Log In"),
		mcp.WithDescription(
			"Log in to Albert Heijn. "+
				"Local mode: opens your browser automatically and waits — returns success once you log in (single call). "+
				"Remote mode: returns a URL to open manually, then call ah_login again to confirm. "+
				"Already authenticated? Returns your name immediately.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleLogin(ctx, deps)
	})
}

func registerLogout(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_logout",
		mcp.WithTitleAnnotation("Albert Heijn: Log Out"),
		mcp.WithDescription(
			"Log out of Albert Heijn by deleting the stored tokens. "+
				"Use this to switch accounts or reset a broken session. "+
				"After logout, call ah_login to authenticate again.",
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return mcp.NewToolResultText("Already logged out (no active session)."), nil
		}
		if err := os.Remove(deps.TokensPath); err != nil && !os.IsNotExist(err) {
			return errResult(fmt.Sprintf("Failed to remove tokens: %v", err)), nil
		}
		// Reset the in-memory client so the next call gets a fresh unauthenticated state.
		deps.ReloadClient() //nolint:errcheck — error just means no tokens, which is expected
		// Also reset any in-progress OAuth flow.
		oauthFlow.Lock()
		oauthFlow.active = false
		oauthFlow.Unlock()
		return mcp.NewToolResultText("Logged out. Call ah_login to authenticate again."), nil
	})
}

func loginSuccess(ctx context.Context, deps Deps) (*mcp.CallToolResult, error) {
	c, err := deps.ReloadClient()
	if err != nil {
		return errResult(fmt.Sprintf("Login succeeded but could not reload client: %v", err)), nil
	}
	member, err := c.GetMember(ctx)
	if err != nil {
		return mcp.NewToolResultText("Login successful!"), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Login successful! Connected as %s %s.", member.FirstName, member.LastName)), nil
}

func handleLogin(ctx context.Context, deps Deps) (*mcp.CallToolResult, error) {
	// Already authenticated — report immediately.
	if deps.IsAuthenticated() {
		c, err := deps.GetClient()
		if err != nil {
			return mcp.NewToolResultText("Already logged in (could not fetch member name)."), nil
		}
		member, err := c.GetMember(ctx)
		if err != nil {
			return mcp.NewToolResultText("Already logged in (could not fetch member name)."), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Already connected as %s %s.", member.FirstName, member.LastName)), nil
	}

	oauthFlow.Lock()

	// Remote mode — second call: check whether the browser callback arrived.
	if oauthFlow.active {
		select {
		case loginErr := <-oauthFlow.done:
			oauthFlow.active = false
			oauthFlow.Unlock()
			if loginErr != nil {
				return errResult(fmt.Sprintf("Login failed: %v", loginErr)), nil
			}
			return loginSuccess(ctx, deps)
		default:
			url := oauthFlow.loginURL
			oauthFlow.Unlock()
			return mcp.NewToolResultText(fmt.Sprintf(
				"Still waiting for browser login. Please open this URL if you haven't yet:\n\n%s\n%s\nThen call ah_login again to confirm.",
				url, loginHint(),
			)), nil
		}
	}

	// Start the OAuth proxy.
	loginURL, done, err := deps.StartOAuthFlow(ctx)
	if err != nil {
		oauthFlow.Unlock()
		return errResult(fmt.Sprintf("Failed to start OAuth flow: %v", err)), nil
	}

	oauthFlow.active = true
	oauthFlow.loginURL = loginURL
	oauthFlow.done = done

	if !deps.RemoteMode {
		// Local mode: open the browser, then BLOCK until the callback arrives.
		// The agent does not need to call ah_login a second time.
		openBrowser(loginURL)
		oauthFlow.Unlock()

		select {
		case loginErr := <-done:
			oauthFlow.Lock()
			oauthFlow.active = false
			oauthFlow.Unlock()
			if loginErr != nil {
				return errResult(fmt.Sprintf("Login failed: %v", loginErr)), nil
			}
			return loginSuccess(ctx, deps)
		case <-ctx.Done():
			oauthFlow.Lock()
			oauthFlow.active = false
			oauthFlow.Unlock()
			return errResult("Login cancelled (context deadline exceeded)."), nil
		}
	}

	// Remote mode: unlock and return the URL — user will call ah_login again after completing.
	oauthFlow.Unlock()
	return mcp.NewToolResultText(fmt.Sprintf(
		"Please open this URL in your browser to log in to Albert Heijn:\n\n%s\n%s\nCall ah_login again once you have completed the login.",
		loginURL, loginHint(),
	)), nil
}

// loginHint returns AH_LOGIN_HINT as an extra paragraph for the remote-mode login
// messages, e.g. which browser the URL has to be opened in.
func loginHint() string {
	if h := os.Getenv("AH_LOGIN_HINT"); h != "" {
		return "\n" + h + "\n"
	}
	return ""
}
