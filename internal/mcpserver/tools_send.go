package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Server) registerSendTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "send_message",
		Description: "Send a WhatsApp text message to a person or group. For group chats use the JID.",
		Title:       "Send message",
		Annotations: writeAnnotations("Send message", false, false, true),
	}, s.handleSendMessage)

	fileTool := &mcp.Tool{Name: "send_file", Description: "Send a file via WhatsApp.",
		Title: "Send file", Annotations: writeAnnotations("Send file", false, false, true)}
	audioTool := &mcp.Tool{Name: "send_audio_message", Description: "Send an audio file as a WhatsApp voice message.",
		Title: "Send audio message", Annotations: writeAnnotations("Send audio message", false, false, true)}
	if s.uploads != nil {
		mcp.AddTool(s.mcp, fileTool, func(ctx context.Context, req *mcp.CallToolRequest, input stagedSendInput) (*mcp.CallToolResult, sendResultDTO, error) {
			return s.handleSendFile(ctx, req, sendFileInput{Recipient: input.Recipient, UploadID: input.UploadID})
		})
		mcp.AddTool(s.mcp, audioTool, func(ctx context.Context, req *mcp.CallToolRequest, input stagedSendInput) (*mcp.CallToolResult, sendResultDTO, error) {
			return s.handleSendAudioMessage(ctx, req, sendAudioMessageInput{Recipient: input.Recipient, UploadID: input.UploadID})
		})
	} else {
		mcp.AddTool(s.mcp, fileTool, s.handleSendFile)
		mcp.AddTool(s.mcp, audioTool, s.handleSendAudioMessage)
	}

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "download_media",
		Description: "Get an authenticated HTTP download path for a WhatsApp attachment.",
		Title:       "Download media",
		Annotations: readOnlyAnnotations("Download media"),
	}, s.handleDownloadMedia)
}

type sendMessageInput struct {
	Recipient string `json:"recipient" jsonschema:"The recipient - either a phone number with country code but no '+' or other symbols, or a JID (e.g. \"123456789@s.whatsapp.net\" or a group JID like \"123456789@g.us\")"`
	Message   string `json:"message" jsonschema:"The message text to send"`
}

func (s *Server) handleSendMessage(ctx context.Context, _ *mcp.CallToolRequest, input sendMessageInput) (*mcp.CallToolResult, sendResultDTO, error) {
	if input.Recipient == "" {
		return nil, sendResultDTO{Success: false, Message: "recipient must be provided"}, nil
	}

	success, message := s.wa.SendMessage(ctx, input.Recipient, input.Message)
	return nil, sendResultDTO{Success: success, Message: message}, nil
}

type stagedSendInput struct {
	Recipient string `json:"recipient" jsonschema:"WhatsApp chat JID or international phone number"`
	UploadID  string `json:"upload_id" jsonschema:"Reference returned by POST /media/uploads"`
}

type sendFileInput struct {
	Recipient string `json:"recipient" jsonschema:"The recipient - either a phone number with country code but no '+' or other symbols, or a JID (e.g. \"123456789@s.whatsapp.net\" or a group JID like \"123456789@g.us\")"`
	UploadID  string `json:"upload_id,omitempty" jsonschema:"Reference returned by POST /media/uploads"`
	MediaPath string `json:"media_path,omitempty" jsonschema:"Absolute path, accessible to this server, to the media file to send"`
}

func (s *Server) handleSendFile(ctx context.Context, _ *mcp.CallToolRequest, input sendFileInput) (*mcp.CallToolResult, sendResultDTO, error) {
	path := input.MediaPath
	if s.uploads != nil {
		s.uploads.mu.Lock()
		defer s.uploads.mu.Unlock()
		s.uploads.prune(time.Now())
		file, ok := s.uploads.files[input.UploadID]
		if !ok || !time.Now().Before(file.expires) {
			return nil, sendResultDTO{Message: "upload_id is missing or expired"}, nil
		}
		path = file.path
	}
	success, message := s.wa.SendFile(ctx, input.Recipient, path)
	return nil, sendResultDTO{Success: success, Message: message}, nil
}

type sendAudioMessageInput struct {
	Recipient string `json:"recipient" jsonschema:"The recipient - either a phone number with country code but no '+' or other symbols, or a JID (e.g. \"123456789@s.whatsapp.net\" or a group JID like \"123456789@g.us\")"`
	UploadID  string `json:"upload_id,omitempty" jsonschema:"Reference returned by POST /media/uploads"`
	MediaPath string `json:"media_path,omitempty" jsonschema:"Absolute path, accessible to this server, to the audio file to send"`
}

func (s *Server) handleSendAudioMessage(ctx context.Context, _ *mcp.CallToolRequest, input sendAudioMessageInput) (*mcp.CallToolResult, sendResultDTO, error) {
	path := input.MediaPath
	if s.uploads != nil {
		s.uploads.mu.Lock()
		defer s.uploads.mu.Unlock()
		s.uploads.prune(time.Now())
		file, ok := s.uploads.files[input.UploadID]
		if !ok || !time.Now().Before(file.expires) {
			return nil, sendResultDTO{Message: "upload_id is missing or expired"}, nil
		}
		path = file.path
	}
	success, message := s.wa.SendAudioMessage(ctx, input.Recipient, path)
	return nil, sendResultDTO{Success: success, Message: message}, nil
}

type downloadMediaInput struct {
	MessageID string `json:"message_id" jsonschema:"The ID of the message containing the media"`
	ChatJID   string `json:"chat_jid" jsonschema:"The JID of the chat containing the message"`
}

func (s *Server) handleDownloadMedia(ctx context.Context, _ *mcp.CallToolRequest, input downloadMediaInput) (*mcp.CallToolResult, downloadResultDTO, error) {
	if s.uploads != nil {
		if err := s.wa.CheckAttachment(ctx, input.MessageID, input.ChatJID); err != nil {
			return nil, downloadResultDTO{Message: err.Error()}, nil
		}
		return nil, downloadResultDTO{Success: true, Message: "download using the MCP bearer token", DownloadPath: attachmentPath(input.ChatJID, input.MessageID)}, nil
	}
	_, _, filePath, err := s.wa.DownloadMedia(ctx, input.MessageID, input.ChatJID)
	if err != nil {
		return nil, downloadResultDTO{Success: false, Message: err.Error()}, nil
	}
	return nil, downloadResultDTO{Success: true, Message: "media downloaded successfully", FilePath: filePath}, nil
}
