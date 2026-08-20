package main

import (
	"fmt"
	"os"

	openai "github.com/xhd2015/llm-proxy/open_ai"
)

func main() {
	if err := openai.Handle(os.Args[1:]); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
