# feiq-cli Web UI

这里保存 `feiq-cli` 内嵌 Web 界面的 Vue 3 + TypeScript 源码。Go 服务代码位于 `cmd/feiq-cli`，两者在同一个仓库中保持目录分离。

界面通过 `/api/paths` 浏览 `feiq-cli` 所在电脑的授权目录，通过 `/api/send-path` 发送选中的文件或目录。允许范围由根目录配置文件中的 `web_roots` 控制，默认是当前用户的 `$HOME`。浏览器上传控件不再使用。

开发前端：

```bash
npm install
npm run dev
```

Vite 默认运行在 `http://127.0.0.1:5173`，并将 `/api` 代理到 `http://127.0.0.1:2426`。另一个终端运行：

```bash
go run ./cmd/feiq-cli web
```

更新嵌入二进制的资源：

```bash
npm test
npm run build
```

构建结果位于 `web/dist`，需要与源码一起提交，以便没有 Node.js 的用户也能直接执行 `go build`。
