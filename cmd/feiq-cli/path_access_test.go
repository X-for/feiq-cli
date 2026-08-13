package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPathAccessResolvesAllowedFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "allowed.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, matchedRoot, err := newPathAccess([]string{root}).Resolve(file)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != file || matchedRoot != root {
		t.Fatalf("Resolve() = (%q, %q), want (%q, %q)", resolved, matchedRoot, file, root)
	}
}

func TestPathAccessRejectsDotDotEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	outside := filepath.Join(parent, "outside.txt")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := newPathAccess([]string{root}).Resolve(filepath.Join(root, "..", "outside.txt")); err == nil {
		t.Fatal(".. escape was accepted")
	}
}

func TestPathAccessRejectsOutsideAbsolutePath(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	file := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := newPathAccess([]string{root}).Resolve(file); err == nil {
		t.Fatal("outside absolute path was accepted")
	}
}

func TestPathAccessRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}

	if _, _, err := newPathAccess([]string{root}).Resolve(filepath.Join(root, "link")); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}

func TestPathAccessAllowsFilesystemRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "visible.txt")
	if err := os.WriteFile(file, []byte("visible"), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, root, err := newPathAccess([]string{string(filepath.Separator)}).Resolve(file)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != file || root != string(filepath.Separator) {
		t.Fatalf("Resolve() = (%q, %q), want (%q, %q)", resolved, root, file, string(filepath.Separator))
	}
}

func TestPathAccessListSortsDirectoriesFirstCaseInsensitively(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zebra", "Alpha"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{"beta.txt": "b", "Apple.txt": "apple"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	listing, err := newPathAccess([]string{root}).List(root)
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, 0, len(listing.Entries))
	gotKinds := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		gotNames = append(gotNames, entry.Name)
		gotKinds = append(gotKinds, entry.Kind)
	}
	if want := []string{"Alpha", "zebra", "Apple.txt", "beta.txt"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("entry names = %q, want %q", gotNames, want)
	}
	if want := []string{"dir", "dir", "file", "file"}; !reflect.DeepEqual(gotKinds, want) {
		t.Fatalf("entry kinds = %q, want %q", gotKinds, want)
	}
	if listing.Entries[2].Size != int64(len(files["Apple.txt"])) {
		t.Fatalf("Apple.txt size = %d", listing.Entries[2].Size)
	}
}

func TestPathAccessListOmitsParentAtRoot(t *testing.T) {
	root := t.TempDir()

	listing, err := newPathAccess([]string{root}).List(root)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Path != root || listing.Root != root {
		t.Fatalf("List() path/root = (%q, %q), want %q", listing.Path, listing.Root, root)
	}
	if !reflect.DeepEqual(listing.Roots, []string{root}) {
		t.Fatalf("roots = %q, want %q", listing.Roots, []string{root})
	}
	if listing.Parent != "" {
		t.Fatalf("parent = %q, want omitted root parent", listing.Parent)
	}
}
