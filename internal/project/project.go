// Package project decides which project a memory belongs to, so that every
// agent working on the same repository shares one project no matter which
// directory or worktree it was started from.
package project

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConfigFile is the file that pins a project name for a directory tree.
const ConfigFile = ".memorycard"

// Resolve returns the project for dir. The order is: the explicit name, a
// ConfigFile in dir or an ancestor, the name of the git remote "origin", the
// name of the git root directory, the name of dir. It never fails: a project
// is always returned, so a write is never blocked by identity.
func Resolve(dir, explicit string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		return p
	}
	if p := fromConfigFile(dir); p != "" {
		return p
	}
	if p := fromGitRemote(dir); p != "" {
		return p
	}
	if root := gitOutput(dir, "rev-parse", "--show-toplevel"); root != "" {
		return filepath.Base(root)
	}
	return filepath.Base(dir)
}

func fromConfigFile(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if f, err := os.Open(filepath.Join(d, ConfigFile)); err == nil {
			line, _ := bufio.NewReader(f).ReadString('\n')
			f.Close()
			if p := strings.TrimSpace(line); p != "" {
				return p
			}
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// fromGitRemote names the project after the last path segment of the origin
// URL, so a clone, a subdirectory and a linked worktree all agree.
func fromGitRemote(dir string) string {
	url := strings.TrimRight(gitOutput(dir, "config", "--get", "remote.origin.url"), "/")
	url = strings.TrimSuffix(url, ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return url
}

func gitOutput(dir string, args ...string) string {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
