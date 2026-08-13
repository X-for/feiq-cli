package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var errPathNotAllowed = errors.New("path is outside configured web roots")

type pathAccess struct {
	roots []string
}

type pathListing struct {
	Path    string      `json:"path"`
	Root    string      `json:"root"`
	Roots   []string    `json:"roots"`
	Parent  string      `json:"parent,omitempty"`
	Entries []pathEntry `json:"entries"`
}

type pathEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int64  `json:"size"`
}

func newPathAccess(roots []string) *pathAccess {
	return &pathAccess{roots: append([]string(nil), roots...)}
}

func (access *pathAccess) Resolve(path string) (resolvedPath string, root string, err error) {
	expanded := expandHomePath(path)
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", "", fmt.Errorf("resolve absolute path %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absolute))
	if err != nil {
		return "", "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	resolved = filepath.Clean(resolved)
	for _, allowedRoot := range access.roots {
		if webRootContains(allowedRoot, resolved) {
			return resolved, allowedRoot, nil
		}
	}
	return "", "", fmt.Errorf("%w: %q", errPathNotAllowed, path)
}

func (access *pathAccess) List(path string) (pathListing, error) {
	resolved, root, err := access.Resolve(path)
	if err != nil {
		return pathListing{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return pathListing{}, fmt.Errorf("stat path %q: %w", path, err)
	}
	if !info.IsDir() {
		return pathListing{}, fmt.Errorf("path %q is not a directory", path)
	}

	directoryEntries, err := os.ReadDir(resolved)
	if err != nil {
		return pathListing{}, fmt.Errorf("read directory %q: %w", path, err)
	}
	entries := make([]pathEntry, 0, len(directoryEntries))
	for _, directoryEntry := range directoryEntries {
		childPath := filepath.Join(resolved, directoryEntry.Name())
		resolvedChild, _, resolveErr := access.Resolve(childPath)
		if resolveErr != nil {
			continue
		}
		childInfo, statErr := os.Stat(resolvedChild)
		if statErr != nil || (!childInfo.IsDir() && !childInfo.Mode().IsRegular()) {
			continue
		}
		kind := "file"
		if childInfo.IsDir() {
			kind = "dir"
		}
		entries = append(entries, pathEntry{
			Name: directoryEntry.Name(),
			Path: resolvedChild,
			Kind: kind,
			Size: childInfo.Size(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		leftDirectory := entries[i].Kind == "dir"
		rightDirectory := entries[j].Kind == "dir"
		if leftDirectory != rightDirectory {
			return leftDirectory
		}
		leftName := strings.ToLower(entries[i].Name)
		rightName := strings.ToLower(entries[j].Name)
		if leftName == rightName {
			return entries[i].Name < entries[j].Name
		}
		return leftName < rightName
	})

	listing := pathListing{
		Path:    resolved,
		Root:    root,
		Roots:   append([]string(nil), access.roots...),
		Entries: entries,
	}
	if resolved != root {
		parent := filepath.Dir(resolved)
		if webRootContains(root, parent) {
			listing.Parent = parent
		}
	}
	return listing, nil
}
