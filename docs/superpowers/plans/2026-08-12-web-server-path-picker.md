# Web Server Path Picker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove browser-side file/directory upload controls and add a secure server-side file browser whose accessible roots default to `$HOME` and can be extended through `web_roots`.

**Architecture:** Configuration resolves `web_roots` into canonical directories. A focused `pathAccess` service owns root containment, symlink-safe authorization, and directory listing; HTTP handlers expose it to the Vue UI and enforce it again before `SendPath`. The Vue path-picker consumes only `/api/paths` and `/api/send-path`; the old multipart upload path is removed.

**Tech Stack:** Go 1.25 (`net/http`, `os`, `path/filepath`), Vue 3, TypeScript 5.9, Vite 7, Vitest, `@vue/test-utils`, happy-dom, embedded assets through `go:embed`.

## Global Constraints

- Default accessible root is the current user's `$HOME`.
- `web_roots` accepts multiple directories and explicitly permits `/`.
- Manual paths, browsed paths, and symlink targets must remain within an allowed root.
- The HTTP service remains local-only by default at `127.0.0.1:2426`; `--allow-remote` remains explicit and unauthenticated.
- Remove `POST /api/upload`, browser file/directory controls, and pasted-image upload behavior.
- Preserve the existing IP Messenger `SendPath` protocol flow.
- Do not use system Python.

---

### Task 1: Parse and Validate `web_roots`

**Files:**
- Modify: `cmd/feiq-cli/config.go`
- Modify: `cmd/feiq-cli/config_test.go`
- Modify: `config.example.json`

**Interfaces:**
- Produces: `appConfig.WebRoots []string` loaded from JSON key `web_roots`.
- Produces: `configuredWebRoots(config appConfig) ([]string, error)`, returning canonical existing directories with `$HOME` fallback, duplicates and contained roots removed.

- [ ] **Step 1: Write failing configuration tests**

Add tests proving expansion, defaulting, `/` support, invalid file rejection, and root compaction:

```go
func TestConfiguredWebRootsDefaultToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := configuredWebRoots(appConfig{})
	if err != nil || !reflect.DeepEqual(got, []string{home}) {
		t.Fatalf("roots=%q err=%v", got, err)
	}
}

func TestConfiguredWebRootsExpandAndCompact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	child := filepath.Join(home, "Documents")
	if err := os.Mkdir(child, 0o700); err != nil { t.Fatal(err) }
	config := appConfig{WebRoots: []string{"~/", "~/Documents", "~/"}}
	got, err := configuredWebRoots(config)
	if err != nil || !reflect.DeepEqual(got, []string{home}) {
		t.Fatalf("roots=%q err=%v", got, err)
	}
}
```

Extend the invalid-config table with a regular-file `web_roots` entry and add a test whose value is `[]string{"/"}`.

- [ ] **Step 2: Run tests and verify RED**

Run: `env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run 'TestConfiguredWebRoots|TestLoadAppConfig'`

Expected: FAIL because `WebRoots` and `configuredWebRoots` do not exist.

- [ ] **Step 3: Implement minimal configuration support**

Add:

```go
type appConfig struct {
	// existing fields
	WebRoots []string `json:"web_roots"`
}

func configuredWebRoots(config appConfig) ([]string, error)
```

For each configured path: expand Home, convert to absolute path, evaluate symlinks, require `info.IsDir()`, remove duplicates, then discard roots already contained by a broader root. If the slice is empty, resolve `os.UserHomeDir()` as the sole root.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run 'TestConfiguredWebRoots|TestLoadAppConfig'`

Expected: PASS.

- [ ] **Step 5: Update the example config and commit**

Add `"web_roots": ["~/"]` to `config.example.json`.

```bash
git add cmd/feiq-cli/config.go cmd/feiq-cli/config_test.go config.example.json
git commit -m "Add configurable Web path roots"
```

### Task 2: Add a Symlink-Safe Path Access Service

**Files:**
- Create: `cmd/feiq-cli/path_access.go`
- Create: `cmd/feiq-cli/path_access_test.go`

**Interfaces:**
- Consumes: canonical directory roots returned by `configuredWebRoots`.
- Produces: `newPathAccess(roots []string) *pathAccess`.
- Produces: `(*pathAccess).Resolve(path string) (resolvedPath string, root string, err error)`.
- Produces: `(*pathAccess).List(path string) (pathListing, error)`.
- Produces JSON types `pathListing` and `pathEntry` with fields `path`, `root`, `roots`, `parent`, `entries`, `name`, `path`, `kind`, and `size`.

- [ ] **Step 1: Write failing authorization and listing tests**

Cover an allowed file, `..` escape, outside absolute path, a symlink inside the root pointing outside, `/` authorization, directory-first sorting, and parent omission at root:

```go
func TestPathAccessRejectsSymlinkEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil { t.Fatal(err) }
	if err := os.Symlink(secret, filepath.Join(root, "link")); err != nil { t.Fatal(err) }
	access := newPathAccess([]string{root})
	if _, _, err := access.Resolve(filepath.Join(root, "link")); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}
```

- [ ] **Step 2: Run tests and verify RED**

Run: `env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run TestPathAccess`

Expected: FAIL because the service and response types do not exist.

- [ ] **Step 3: Implement canonical containment and listing**

`Resolve` expands `~`, obtains an absolute cleaned path, calls `filepath.EvalSymlinks`, and accepts it only when `filepath.Rel(root, resolved)` is neither `..` nor prefixed by `../`. `List` requires a directory, reads entries without following children unnecessarily, resolves every returned child before including it, omits inaccessible/out-of-root entries, sorts directories before files using case-insensitive names, and returns a parent only if it remains inside the same root.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run: `env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run TestPathAccess`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/feiq-cli/path_access.go cmd/feiq-cli/path_access_test.go
git commit -m "Add secure Web path access service"
```

### Task 3: Expose Path Browsing and Enforce Roots on Sending

**Files:**
- Modify: `cmd/feiq-cli/api.go`
- Modify: `cmd/feiq-cli/api_test.go`

**Interfaces:**
- Consumes: `*pathAccess` from Task 2.
- Adds: `GET /api/paths?path=...` handled by `(*webApp).handlePaths`.
- Changes: `(*webApp).handleSendPath` resolves and authorizes `body.Path` before `os.Stat` and `startPathTransfer`.
- Removes: `POST /api/upload`, `handleUpload`, `safeUploadPath`, `saveMultipartFile`, multipart imports, and the upload-size constant.

- [ ] **Step 1: Write failing API tests**

Extend `newTestWebApp` with a Home-scoped `pathAccess`. Add tests that GET the default root, browse a child directory, reject an outside path, reject an outside `send-path`, and accept an allowed path. Assert that `POST /api/upload` returns 404/405:

```go
func TestWebPathsRejectOutsideRoot(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	app := newTestWebAppAtRoot(t, &fakeWebSession{}, root)
	request := httptest.NewRequest(http.MethodGet, "/api/paths?path="+url.QueryEscape(outside), nil)
	response := httptest.NewRecorder()
	app.routes().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
```

- [ ] **Step 2: Run API tests and verify RED**

Run: `env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run 'TestWebPaths|TestWebSendPath|TestWebUploadRemoved'`

Expected: FAIL because `/api/paths` is absent and send-path is unrestricted.

- [ ] **Step 3: Wire roots into HTTP startup and implement handlers**

In `httpMode`, call `configuredWebRoots(config)` before starting the session and store `paths: newPathAccess(roots)` on `webApp`. Register `GET /api/paths`. An empty `path` query lists the first root. Convert authorization errors to 403, missing/not-directory errors to 400/404, and unexpected filesystem failures to 500.

Delete the multipart upload endpoint and helpers. In `handleSendPath`, use the resolved canonical path for both `os.Stat` and `startPathTransfer`.

- [ ] **Step 4: Run API and full Go tests and verify GREEN**

Run:

```bash
env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test ./cmd/feiq-cli -run 'TestWebPaths|TestWebSendPath|TestWebUploadRemoved'
env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test -race ./...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/feiq-cli/api.go cmd/feiq-cli/api_test.go
git commit -m "Add Web server path browsing API"
```

### Task 4: Replace Upload Controls with the Server Path Picker

**Files:**
- Modify: `web/package.json`
- Modify: `web/package-lock.json`
- Create: `web/src/path-picker.ts`
- Create: `web/src/path-picker.test.ts`
- Create: `web/src/App.test.ts`
- Modify: `web/src/App.vue`
- Modify: `web/src/style.css`
- Modify: `web/vite.config.ts`

**Interfaces:**
- Consumes: `GET /api/paths` response from Task 3.
- Produces: TypeScript types `PathListing` and `PathEntry` in `path-picker.ts`.
- Produces: `pathBreadcrumbs(root: string, path: string): Array<{ label: string; path: string }>` that never emits ancestors above `root`.
- Sends selection through existing `POST /api/send-path` JSON `{to, path, kind}`.

- [ ] **Step 1: Add the frontend test runner**

Add dev dependencies `vitest`, `@vue/test-utils`, and `happy-dom`; add script `"test": "vitest run"`; configure Vite test environment as `happy-dom`.

- [ ] **Step 2: Write failing pure-function and component tests**

`path-picker.test.ts` verifies root-bounded breadcrumbs for `/Users/zaq`, `/Users/zaq/Documents/images`, and `/`.

`App.test.ts` mounts `App`, mocks `fetch` and `EventSource`, then asserts:

```ts
expect(wrapper.text()).not.toContain('＋ 文件')
expect(wrapper.text()).not.toContain('▦ 目录')
await wrapper.get('[data-test="open-path-picker"]').trigger('click')
expect(fetch).toHaveBeenCalledWith(expect.stringContaining('/api/paths'))
expect(wrapper.get('[data-test="path-picker"]').exists()).toBe(true)
```

Add a test selecting an entry and asserting `/api/send-path` receives its server path and kind.

- [ ] **Step 3: Run tests and verify RED**

Run: `cd web && npm test`

Expected: FAIL because helpers/data-test elements do not exist and old upload buttons remain.

- [ ] **Step 4: Implement the minimal picker**

Remove `pendingFiles`, file input refs, `chooseFiles`, `selectedFiles`, `handlePaste`, `uploadPending`, pending-upload markup, hidden file inputs, and composer action buttons.

Add picker state for listing, loading, selected entry, kind, and manual path. Opening loads `/api/paths`; root selection, parent, breadcrumbs, and directory entry activation load another listing. Selecting a file sets kind `file`; selecting a directory or current directory sets kind `dir`. Sending calls `/api/send-path`, clears state, and closes the panel on accepted response.

Render the picker with stable `data-test` hooks, accessible labels, keyboard-operable buttons, a scrollable entry list, empty/loading/error states, root selector, breadcrumbs, manual path field, and cancel/send actions.

- [ ] **Step 5: Implement responsive styles**

Replace `.path-panel`, `.pending-upload`, `.composer-actions`, and `.hidden-input` rules with `.path-picker`, `.path-toolbar`, `.path-breadcrumbs`, `.path-list`, `.path-entry`, and `.path-picker-actions`. At `720px` stack the toolbar and keep the list height within `min(42dvh, 360px)`; at `440px` make action buttons full-width. Keep the message composer as `minmax(0, 1fr) auto` at all widths.

- [ ] **Step 6: Run frontend tests, typecheck, and build**

Run:

```bash
cd web
npm test
npm run typecheck
npm run build
```

Expected: all commands PASS and `web/dist` contains the new hashed assets.

- [ ] **Step 7: Commit**

```bash
git add web/package.json web/package-lock.json web/src web/vite.config.ts web/dist
git commit -m "Replace Web uploads with server path picker"
```

### Task 5: Synchronize Documentation and Verify the Release Surface

**Files:**
- Modify: `README.md`
- Modify: `web/README.md`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/release.yml`

**Interfaces:**
- Documents `web_roots`, `$HOME` default, `/` behavior, and the single server-path sending flow.
- CI consumes `npm test`, `npm run build`, and the existing Go checks.

- [ ] **Step 1: Update documentation and CI**

Add `web_roots` to the configuration table and examples. Explain that manual and browsed paths are restricted to those roots, `/` grants full filesystem browsing, and remote Web access exposes these capabilities. Remove claims about browser file/directory selection and pasted images.

In both workflows, run `npm test` before `npm run build` in `web/`.

- [ ] **Step 2: Run the complete verification suite**

```bash
cd web
npm ci
npm test
npm run typecheck
npm run build
cd ..
git diff --exit-code -- web/dist
env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go test -race ./...
env GOCACHE=/private/tmp/feiq-cli-go-cache GOTMPDIR=/private/tmp go vet ./...
test -z "$(gofmt -l .)"
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 3: Verify four release targets**

Build with `CGO_ENABLED=0` for `darwin/amd64`, `darwin/arm64`, `linux/amd64`, and `linux/arm64`, writing binaries under `/private/tmp`; each command must exit 0.

- [ ] **Step 4: Commit documentation and workflow changes**

```bash
git add README.md web/README.md .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "Document secure Web path selection"
```

- [ ] **Step 5: Push and verify CI**

```bash
git push origin main
```

Confirm the new `main` GitHub Actions run completes with conclusion `success` before reporting completion.
