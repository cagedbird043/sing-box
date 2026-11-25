//go:build !windows

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func readPrivateURLFile(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("configuration URL file must be a regular file owned by the service user with mode 0600")
	}
	return io.ReadAll(file)
}

func protectURLFile(file *os.File) error {
	return file.Chmod(0o600)
}

func checkServiceDirectory(path string, create bool) error {
	_, err := checkUnixServicePath(path, true, create, true, 0)
	return err
}

func checkServiceControlDirectory(path string) error {
	info, err := checkUnixServicePath(path, true, false, false, 0)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("service control directory must have mode 0700")
	}
	return nil
}

func checkServiceExecutable(path string) error {
	_, err := checkUnixServicePath(path, false, false, true, 0)
	return err
}

func checkServiceFile(path string) error {
	_, err := checkUnixServicePath(path, false, false, false, 0)
	return err
}

func checkUnixServicePath(path string, directory, create, allowLink bool, links int) (os.FileInfo, error) {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent != path {
		if _, err := checkUnixServicePath(parent, true, create, true, links); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err = os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return nil, err
	}
	if stat := info.Sys().(*syscall.Stat_t); stat.Uid != 0 {
		return nil, fmt.Errorf("%s is not owned by root", path)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if !allowLink || links == 40 {
			return nil, fmt.Errorf("unsafe symbolic link at %s", path)
		}
		destination, err := os.Readlink(path)
		if err != nil {
			return nil, err
		}
		var hasComponent bool
		for component := range strings.SplitSeq(destination, "/") {
			switch component {
			case "", ".":
			case "..":
				if hasComponent {
					return nil, fmt.Errorf("unsafe symbolic link at %s", path)
				}
			default:
				hasComponent = true
			}
		}
		if !filepath.IsAbs(destination) {
			resolvedParent, err := filepath.EvalSymlinks(parent)
			if err != nil {
				return nil, err
			}
			destination = filepath.Join(resolvedParent, destination)
		}
		return checkUnixServicePath(destination, directory, false, true, links+1)
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unexpected file type at %s", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s permits unprivileged writes", path)
	}
	if runtime.GOOS == "darwin" {
		if err := checkDarwinServiceACL(path); err != nil {
			return nil, err
		}
	}
	return info, nil
}

func checkDarwinServiceACL(path string) error {
	// Darwin ACL grants are independent of the mode bits.
	command := exec.Command("/bin/ls", "-ldeB", path)
	command.Env = []string{"LC_ALL=C"}
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("check access rules for %s: %w", path, err)
	}
	_, entries, _ := strings.Cut(string(output), "\n")
	for entry := range strings.SplitSeq(entries, "\n") {
		principal, rights, permit := strings.Cut(entry, " allow ")
		if !permit || strings.HasSuffix(principal, " user:root") {
			continue
		}
		for right := range strings.SplitSeq(rights, ",") {
			switch right {
			case "read", "list", "search", "execute", "readattr", "readextattr", "readsecurity",
				"inherited", "file_inherit", "directory_inherit", "limit_inherit", "only_inherit":
			default:
				return fmt.Errorf("%s has an ACL permitting unprivileged writes", path)
			}
		}
	}
	return nil
}
