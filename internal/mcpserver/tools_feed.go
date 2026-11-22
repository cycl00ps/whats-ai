package mcpserver

import (
	"context"

	"github.com/goodtune/whatsapp-mcp/internal/wa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type subscriptions struct {
	ChatJIDs []string `json:"chat_jids"`
}

type readUpdatesInput struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (s *Server) registerWorkerTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "get_subscriptions", Description: "List selected chat JIDs."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, subscriptions, error) {
			return nil, subscriptions{ChatJIDs: s.wa.Subscriptions()}, nil
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "set_subscriptions", Description: "Replace the selected chat JIDs. An empty list pauses delivery."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input subscriptions) (*mcp.CallToolResult, subscriptions, error) {
			jids, err := s.wa.SetSubscriptions(ctx, input.ChatJIDs)
			return nil, subscriptions{ChatJIDs: jids}, err
		})
	mcp.AddTool(s.mcp, &mcp.Tool{Name: "read_updates", Description: "Read selected live messages in receipt order. Save the page before advancing the cursor."},
		func(ctx context.Context, _ *mcp.CallToolRequest, input readUpdatesInput) (*mcp.CallToolResult, wa.UpdatePage, error) {
			page, err := s.wa.ReadUpdates(ctx, input.Cursor, input.Limit)
			return nil, page, err
		})
}
