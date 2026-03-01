package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// execSession holds the pod target for a pending exec WebSocket connection.
type execSession struct {
	namespace string
	name      string
	container string
	shell     string // "auto", "bash", "sh" — which shell strategy to use
}

var (
	execSessions   = map[string]execSession{}
	execSessionsMu sync.Mutex
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// termSizeQueue bridges resize messages from the browser to remotecommand.
type termSizeQueue struct {
	ch chan remotecommand.TerminalSize
}

func (q *termSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// execCommandForShell returns the command to run in the pod for the given shell preference.
// Portainer-style: "auto" tries script (PTY+echo) then bash then sh; "bash" skips script; "sh" is minimal.
func execCommandForShell(shell string) []string {
	base := "export TERM=xterm-256color; "
	switch shell {
	case "sh":
		// Minimal: only sh -i. Works on Alpine, BusyBox, distroless-with-sh.
		return []string{"/bin/sh", "-c", base + "exec sh -i"}
	case "bash":
		// No script: avoid breakage on images where script is missing or incompatible (e.g. BusyBox).
		return []string{"/bin/sh", "-c", base + "exec bash -i 2>/dev/null || exec sh -i"}
	default:
		// auto: try script (best UX), then bash -i, then sh -i.
		return []string{"/bin/sh", "-c", base + "(script -q -c \"exec bash -i\" /dev/null) 2>/dev/null || (exec bash -i) 2>/dev/null || exec sh -i"}
	}
}

// CreateExecSession handles POST /pods/{namespace}/{name}/exec.
// Body (optional JSON): {"container": "<name>", "shell": "auto"|"bash"|"sh"}
// Returns: {"sessionId": "<uuid>"}
func (h *Handler) CreateExecSession(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace, name := vars["namespace"], vars["name"]

	var body struct {
		Container string `json:"container"`
		Shell     string `json:"shell"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	shell := body.Shell
	if shell != "bash" && shell != "sh" {
		shell = "auto"
	}

	sessionID := uuid.New().String()
	execSessionsMu.Lock()
	execSessions[sessionID] = execSession{
		namespace: namespace,
		name:      name,
		container: body.Container,
		shell:     shell,
	}
	execSessionsMu.Unlock()

	slog.Info("exec session created", "sessionId", sessionID, "namespace", namespace, "pod", name)
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": sessionID})
}

// ExecPodWS handles GET /pods/exec/ws?sessionId=<uuid>.
// Upgrades the connection to WebSocket, then bridges browser ↔ pod exec.
//
// Browser → server message protocol:
//   - Binary frame or plain text  →  stdin
//   - JSON {"type":"resize","cols":N,"rows":N}  →  terminal resize
//   - JSON {"type":"ping"}        →  pong response
//   - JSON {"type":"inject","data":"..."}  →  write literal string to stdin
//
// Server → browser message protocol:
//   - Binary frames  →  stdout/stderr (merged when TTY is active)
//   - JSON {"type":"connected"}
//   - JSON {"type":"disconnected"}
//   - JSON {"type":"pong"}
//   - JSON {"type":"error","message":"..."}
func (h *Handler) ExecPodWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		http.Error(w, `{"error":"sessionId required"}`, http.StatusBadRequest)
		return
	}

	execSessionsMu.Lock()
	session, ok := execSessions[sessionID]
	if ok {
		delete(execSessions, sessionID)
	}
	execSessionsMu.Unlock()

	if !ok {
		http.Error(w, `{"error":"session not found or expired"}`, http.StatusNotFound)
		return
	}

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Clear any deadlines the http.Server set before the handler ran.
	// After Hijack those deadlines are still on the TCP connection and would
	// kill long-lived WebSocket sessions.
	conn.SetReadDeadline(time.Time{})
	conn.SetWriteDeadline(time.Time{})

	// gorilla/websocket requires a single concurrent writer — use a mutex.
	var writeMu sync.Mutex
	writeMsg := func(msgType int, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteMessage(msgType, data)
	}
	sendJSON := func(v any) {
		data, _ := json.Marshal(v)
		writeMsg(websocket.TextMessage, data) //nolint:errcheck
	}

	// Build the exec request URL targeting the pod.
	command := execCommandForShell(session.shell)
	req := h.k8s.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(session.name).
		Namespace(session.namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: session.container,
			Command:   command,
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(h.cfg, "POST", req.URL())
	if err != nil {
		slog.Error("failed to create SPDY executor", "error", err)
		sendJSON(map[string]string{"type": "error", "message": "failed to create executor: " + err.Error()})
		return
	}

	// stdin: browser → stdinW → stdinR → k8s exec
	stdinR, stdinW := io.Pipe()

	// stdout: k8s exec → stdoutW → stdoutR → browser
	stdoutR, stdoutW := io.Pipe()

	// resize channel — pre-seeded with a sane default so the PTY is correctly
	// sized from the very first frame, before the browser sends its actual size.
	resizeCh := make(chan remotecommand.TerminalSize, 8)
	resizeCh <- remotecommand.TerminalSize{Width: 80, Height: 24}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Goroutine: forward stdout pipe → browser as binary WebSocket frames.
	go func() {
		defer stdoutR.Close()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := stdoutR.Read(buf)
			if n > 0 {
				if err := writeMsg(websocket.BinaryMessage, buf[:n]); err != nil {
					return // browser gone
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Goroutine: run the exec stream (blocks until done or ctx cancelled).
	execDone := make(chan error, 1)
	go func() {
		streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:             stdinR,
			Stdout:            stdoutW,
			Stderr:            stdoutW, // merged into stdout when TTY=true
			Tty:               true,
			TerminalSizeQueue: &termSizeQueue{ch: resizeCh},
		})
		stdoutW.Close()
		close(resizeCh)
		execDone <- streamErr
	}()

	// Goroutine: watch for exec completion and notify the browser.
	// Calling conn.Close() here forces conn.ReadMessage() in the main loop
	// to return, which is the cleanest way to unblock it.
	go func() {
		streamErr := <-execDone
		if streamErr != nil && ctx.Err() == nil {
			// Only surface real errors — ignore context-cancelled noise.
			slog.Error("exec stream error", "sessionId", sessionID, "error", streamErr)
			sendJSON(map[string]string{"type": "error", "message": streamErr.Error()})
		}
		sendJSON(map[string]string{"type": "disconnected"})
		writeMu.Lock()
		conn.Close()
		writeMu.Unlock()
	}()

	// Tell the browser the exec stream is live.
	sendJSON(map[string]string{"type": "connected", "sessionId": sessionID})
	slog.Info("exec session started", "sessionId", sessionID, "namespace", session.namespace, "pod", session.name)

	// Main loop: read messages from the browser and dispatch them.
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			break // browser closed, or conn.Close() called by exec-done goroutine
		}

		if msgType == websocket.TextMessage {
			var msg struct {
				Type string `json:"type"`
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
				Data string `json:"data"`
			}
			if json.Unmarshal(data, &msg) == nil {
				switch msg.Type {
				case "resize":
					select {
					case resizeCh <- remotecommand.TerminalSize{Width: msg.Cols, Height: msg.Rows}:
					default:
					}
					continue
				case "ping":
					sendJSON(map[string]string{"type": "pong"})
					continue
				case "inject":
					if msg.Data != "" {
						stdinW.Write([]byte(msg.Data)) //nolint:errcheck
					}
					continue
				}
			}
			// Unrecognised text or raw input → stdin
			stdinW.Write(data) //nolint:errcheck
		} else {
			stdinW.Write(data) //nolint:errcheck
		}
	}

	// Browser disconnected — cancel the exec context and drain stdin.
	cancel()
	stdinW.Close()
	// execDone is consumed by the exec-done goroutine above; don't read it again.

	slog.Info("exec session ended", "sessionId", sessionID)
}
