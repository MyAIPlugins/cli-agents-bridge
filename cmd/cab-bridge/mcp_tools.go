package main

import (
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
)

// The tool registry of `cab-bridge mcp` and the server built from it.
//
// The descriptions are the one place where the model on the other side is told how
// the bridge behaves, so each says what it does, what it does NOT do, and where the
// text it returns comes from. A description is also a claim about the code, and the
// tests that pin the behaviours it names are listed next to the tool they belong to.

const (
	// mcpDrainTimeout is how long a shutdown waits for calls still running.
	mcpDrainTimeout = 5 * time.Second
	// mcpLineSlack is what a request line carries beyond the message itself: the
	// envelope, the tool name and the other arguments.
	mcpLineSlack = 4096
	// jsonEscapeFactor is the worst growth of text once JSON-escaped (\u00XX).
	jsonEscapeFactor = 6
)

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func (a *mcpApp) tools() []mcp.Tool {
	return []mcp.Tool{
		{
			Name: "join",
			Description: "Join a project as its architect. With no arguments it lists the projects whose val has opened the bridge to Claude Desktop " +
				"(a val does that with `cab-bridge desktop open`, and it expires). With `project` it joins that one. " +
				"In a joined project you can talk to its val and to nobody else. Calling it again for the same project changes nothing.",
			InputSchema: objectSchema(map[string]any{
				"project": stringProp("A project name from the list. Leave it out to see the list."),
			}),
			Handler: a.toolJoin,
		},
	}
}

// newServer builds the MCP server over this app's tools, with the limits from the
// configuration. The longest legitimate request is a message of the configured
// maximum, escaped as badly as JSON can, plus its envelope.
func (a *mcpApp) newServer() (*mcp.Server, error) {
	return mcp.New(mcp.Config{
		Name:           "cab-bridge",
		Version:        version,
		Tools:          a.tools(),
		MaxLineBytes:   jsonEscapeFactor*a.cfg.MaxMessageBytes + mcpLineSlack,
		MaxResultBytes: a.cfg.MCPMaxResultBytes,
		DrainTimeout:   mcpDrainTimeout,
		Logf:           a.logf,
	})
}
