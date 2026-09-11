package main

import (
	"errors"
	"fmt"
	"os"

	openai "github.com/xhd2015/llm-proxy/open_ai"
)

func main() {
	if err := openai.Handle(os.Args[1:]); err != nil {
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			os.Exit(exitCoder.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
