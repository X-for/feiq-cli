package main

import (
	"fmt"
	"io"
	"runtime"
)

var (
	appVersion = "dev"
	appCommit  = "unknown"
	appDate    = "unknown"
)

func printVersion(writer io.Writer) {
	fmt.Fprintf(writer, "feiq-cli %s\n", appVersion)
	fmt.Fprintf(writer, "commit: %s\n", appCommit)
	fmt.Fprintf(writer, "built: %s\n", appDate)
	fmt.Fprintf(writer, "go: %s\n", runtime.Version())
	fmt.Fprintf(writer, "platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
}
