package ipmsg

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type dirRecord struct {
	Name string
	Size int64
	Attr uint64
}

func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func writeDirectoryStream(w io.Writer, root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return writeDirectoryRecursive(w, root, info.Name())
}

func writeDirectoryRecursive(w io.Writer, path, name string) error {
	if err := writeDirHeader(w, dirRecord{Name: name, Attr: FileDirectory}); err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if err := writeDirectoryRecursive(w, child, entry.Name()); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			if err := writeDirHeader(w, dirRecord{
				Name: entry.Name(),
				Size: info.Size(),
				Attr: FileRegular,
			}); err != nil {
				return err
			}
			file, err := os.Open(child)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(w, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	return writeDirHeader(w, dirRecord{Name: ".", Attr: FileRetParent})
}

func writeDirHeader(w io.Writer, record dirRecord) error {
	name := strings.ReplaceAll(record.Name, ":", "::")
	// FeiQ-compatible IPMsg peers read an exact four-digit hexadecimal header
	// length. The length includes the four digits and their trailing colon.
	body := fmt.Sprintf("%s:%09x:%x:", name, record.Size, record.Attr)
	length := 5 + len(body)
	if length > 0xffff {
		return fmt.Errorf("directory header is too large: %d bytes", length)
	}
	header := fmt.Sprintf("%04x:%s", length, body)
	_, err := io.WriteString(w, header)
	return err
}

func receiveDirectoryStream(r io.Reader, outputDir string) (string, error) {
	reader := bufio.NewReader(r)
	var stack []string
	var root string
	for {
		record, err := readDirHeader(reader)
		if err != nil {
			if err == io.EOF && len(stack) == 0 && root != "" {
				return root, nil
			}
			return "", err
		}
		switch record.Attr & 0xff {
		case FileDirectory:
			name, err := safeName(record.Name)
			if err != nil {
				return "", err
			}
			parent := outputDir
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			dir, err := uniquePath(parent, name)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			if root == "" {
				root = dir
			}
			stack = append(stack, dir)
		case FileRegular:
			if len(stack) == 0 {
				return "", fmt.Errorf("directory stream contains a file outside a directory")
			}
			name, err := safeName(record.Name)
			if err != nil {
				return "", err
			}
			path, err := uniquePath(stack[len(stack)-1], name)
			if err != nil {
				return "", err
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err != nil {
				return "", err
			}
			_, copyErr := io.CopyN(file, reader, record.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return "", copyErr
			}
			if closeErr != nil {
				return "", closeErr
			}
		case FileRetParent:
			if len(stack) == 0 {
				return "", fmt.Errorf("unexpected RETPARENT record")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return root, nil
			}
		default:
			return "", fmt.Errorf("unsupported directory entry attribute %#x", record.Attr)
		}
	}
}

func readDirHeader(r *bufio.Reader) (dirRecord, error) {
	lengthField, err := r.ReadString(':')
	if err != nil {
		return dirRecord{}, err
	}
	length, err := strconv.ParseInt(strings.TrimSuffix(lengthField, ":"), 16, 64)
	if err != nil {
		return dirRecord{}, fmt.Errorf("invalid directory header length: %w", err)
	}
	remaining := length - int64(len(lengthField))
	if remaining < 0 || remaining > 1024*1024 {
		return dirRecord{}, fmt.Errorf("invalid directory header size %d", length)
	}
	body := make([]byte, remaining)
	if _, err := io.ReadFull(r, body); err != nil {
		return dirRecord{}, err
	}
	fields := splitEscapedColon(body)
	if len(fields) < 3 {
		return dirRecord{}, fmt.Errorf("invalid directory header %q", body)
	}
	size, err := strconv.ParseInt(string(fields[1]), 16, 64)
	if err != nil {
		return dirRecord{}, fmt.Errorf("invalid directory entry size: %w", err)
	}
	attr, err := strconv.ParseUint(string(fields[2]), 16, 64)
	if err != nil {
		return dirRecord{}, fmt.Errorf("invalid directory entry attribute: %w", err)
	}
	return dirRecord{Name: decodeText(fields[0]), Size: size, Attr: attr}, nil
}

func safeName(name string) (string, error) {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", fmt.Errorf("unsafe received filename %q", name)
	}
	return name, nil
}

func uniquePath(parent, name string) (string, error) {
	path := filepath.Join(parent, name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return path, nil
	} else if err != nil {
		return "", err
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(parent, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("cannot allocate a unique path for %q", name)
}
