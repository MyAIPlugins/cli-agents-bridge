package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

func runAsk(args []string) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	to := fs.String("to", "", "target session ID (required)")
	msgType := fs.String("type", message.TypeQuery, "message type (query|response|ping|notify|event)")
	content := fs.String("content", "", "message content (mutually exclusive with --file)")
	contentFile := fs.String("file", "", "read content from file (avoids shell quoting for large payloads, FRIC-2)")
	inReplyTo := fs.String("in-reply-to", "", "msg-... ID this message replies to (required for type=response)")
	allowMesh := fs.Bool("allow-mesh", false, "allow esc→esc routing (BUG-3 override)")
	sessionIDFlag := fs.String("session-id", "", "sender session ID (default: longest-prefix lookup from cwd)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if *to == "" {
		return errors.New("ask: --to required")
	}
	if err := security.ValidateSessionID(*to); err != nil {
		return fmt.Errorf("ask: --to: %w", err)
	}
	if *content != "" && *contentFile != "" {
		return errors.New("ask: --content and --file are mutually exclusive")
	}

	body := *content
	if *contentFile != "" {
		data, err := os.ReadFile(*contentFile)
		if err != nil {
			return fmt.Errorf("ask: read --file %q: %w", *contentFile, err)
		}
		body = string(data)
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	sid, err := resolveSessionID(mgr, *sessionIDFlag)
	if err != nil {
		return err
	}
	senderManifest, err := mgr.LoadManifest(sid)
	if err != nil {
		return fmt.Errorf("ask: load sender manifest: %w", err)
	}

	targetManifest, err := mgr.LoadManifest(*to)
	if err != nil {
		return fmt.Errorf("ask: load target manifest %q: %w", *to, err)
	}

	if err := routing.ValidateSendPair(senderManifest.Role, targetManifest.Role, *allowMesh); err != nil {
		return err
	}

	msgID, err := message.GenerateMessageID()
	if err != nil {
		return fmt.Errorf("ask: %w", err)
	}

	var inReplyToPtr *string
	if *inReplyTo != "" {
		s := *inReplyTo
		inReplyToPtr = &s
	}

	m := &message.Message{
		ID:            msgID,
		SchemaVersion: message.SchemaVersionV2,
		From:          sid,
		FromRole:      senderManifest.Role,
		FromAgentName: senderManifest.AgentName,
		To:            *to,
		ToRole:        targetManifest.Role,
		Type:          *msgType,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       body,
		InReplyTo:     inReplyToPtr,
		Metadata: message.Metadata{
			FromProject:     senderManifest.ProjectName,
			ProcessingState: message.StatusPending,
		},
	}

	data, err := message.EncodeStrict(m, cfg.MaxMessageBytes)
	if err != nil {
		return err
	}

	targetInbox := filepath.Join(cfg.DataDir, "sessions", *to, "inbox")
	if err := os.MkdirAll(targetInbox, 0o700); err != nil {
		return fmt.Errorf("ask: mkdir target inbox: %w", err)
	}
	dst := filepath.Join(targetInbox, msgID+".json")
	if err := transportfs.AtomicWriteBytes(dst, data, 0o600); err != nil {
		return fmt.Errorf("ask: write message: %w", err)
	}

	// Print the message ID on stdout for caller to capture (e.g. for a
	// subsequent `cab-bridge receive --msg-id=<id>`).
	fmt.Println(msgID)
	return nil
}
