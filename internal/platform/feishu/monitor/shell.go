package monitor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func runFeishuEventCommands(ctx context.Context, sender textSender, eventName, chatID string, scripts []string, env map[string]string) error {
	for i, script := range scripts {
		out, err := runShellScript(ctx, script, env)
		if err != nil {
			return fmt.Errorf("run feishu event %s command %d: %w", eventName, i+1, err)
		}
		text := strings.TrimRight(out, "\r\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		if strings.TrimSpace(chatID) == "" {
			feishuLog.Warn(ctx, "event command stdout ignored because event %s has no chat_id", eventName)
			continue
		}
		if err := sender.SendText(ctx, chatID, text); err != nil {
			return err
		}
	}
	return nil
}

func runShellScript(ctx context.Context, script string, env map[string]string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = os.Environ()
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return stdout.String(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.String(), err
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		feishuLog.Warn(ctx, "event command stderr: %s", msg)
	}
	return stdout.String(), nil
}
