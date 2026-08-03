package cdp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

const webViewSocketPrefix = "@webview_devtools_remote_"

type ambiguousWebViewSocketError struct {
	process string
	pids    []int
	sockets []string
}

func (e *ambiguousWebViewSocketError) Error() string {
	return fmt.Sprintf(
		"multiple WebView DevTools sockets match process %q (PIDs %v): %s",
		e.process,
		e.pids,
		strings.Join(e.sockets, ", "),
	)
}

func normalizeAppProcess(appPackage, appProcess string) (string, error) {
	if appProcess == "" {
		return appPackage, nil
	}
	if strings.HasPrefix(appProcess, ":") {
		appProcess = appPackage + appProcess
	}
	if strings.HasPrefix(appProcess, ".") || strings.HasSuffix(appProcess, ".") ||
		strings.Contains(appProcess, "..") || strings.HasSuffix(appProcess, ":") ||
		strings.Count(appProcess, ":") > 1 {
		return "", fmt.Errorf("invalid app process %q", appProcess)
	}
	for _, char := range appProcess {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return "", fmt.Errorf("invalid app process %q", appProcess)
	}
	return appProcess, nil
}

func webViewSocketContext(ctx context.Context, adbClient *adb.Client, appProcess string) (string, error) {
	pids, err := appPIDsContext(ctx, adbClient, appProcess)
	if err != nil {
		return "", err
	}

	unixTable, err := adbClient.ShellContext(ctx, "cat /proc/net/unix")
	if err != nil {
		return "", fmt.Errorf("list WebView DevTools sockets for process %q: %w", appProcess, err)
	}
	return selectWebViewSocket(appProcess, pids, unixTable)
}

func appPIDsContext(ctx context.Context, adbClient *adb.Client, appProcess string) ([]int, error) {
	out, err := adbClient.ShellContext(ctx, "pidof "+appProcess)
	if err != nil {
		return nil, fmt.Errorf("get PIDs of process %q: %w", appProcess, err)
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return nil, fmt.Errorf("get PIDs of process %q: pidof returned no PIDs", appProcess)
	}

	seen := make(map[int]struct{}, len(fields))
	pids := make([]int, 0, len(fields))
	for _, field := range fields {
		pid, parseErr := strconv.Atoi(field)
		if parseErr != nil || pid <= 0 {
			return nil, fmt.Errorf("parse pidof output %q for process %q: invalid PID %q", out, appProcess, field)
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return pids, nil
}

func selectWebViewSocket(appProcess string, pids []int, unixTable string) (string, error) {
	matches := make(map[string]struct{})
	for _, line := range strings.Split(unixTable, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		path := fields[len(fields)-1]
		if !strings.HasPrefix(path, webViewSocketPrefix) {
			continue
		}
		for _, pid := range pids {
			if webViewSocketMatchesPID(path, pid) {
				matches[strings.TrimPrefix(path, "@")] = struct{}{}
				break
			}
		}
	}

	sockets := make([]string, 0, len(matches))
	for socket := range matches {
		sockets = append(sockets, socket)
	}
	sort.Strings(sockets)
	if len(sockets) == 0 {
		return "", fmt.Errorf("no WebView DevTools socket for process %q (PIDs %v)", appProcess, pids)
	}
	if len(sockets) > 1 {
		return "", &ambiguousWebViewSocketError{process: appProcess, pids: pids, sockets: sockets}
	}
	return sockets[0], nil
}

func webViewSocketMatchesPID(socketPath string, pid int) bool {
	pidText := strconv.Itoa(pid)
	tail := strings.TrimPrefix(socketPath, webViewSocketPrefix)
	if !strings.HasSuffix(tail, pidText) {
		return false
	}
	prefixLength := len(tail) - len(pidText)
	if prefixLength == 0 {
		return true
	}
	preceding := tail[prefixLength-1]
	return preceding < '0' || preceding > '9'
}
