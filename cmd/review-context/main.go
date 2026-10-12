package main

import (
	"data-overmind/internal/reviewcontext"
	"flag"
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("review-context", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "markdown", "output format: markdown or json")
	reviewPath := flags.String("review", "", "review JSON path (defaults to document.html.review.json)")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: review-context [--format markdown|json] [--review review.json] document.html")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 || (*format != "markdown" && *format != "json") {
		flags.Usage()
		return 2
	}

	var result reviewcontext.Result
	var err error
	if *reviewPath == "" {
		result, err = reviewcontext.Analyze(flags.Arg(0))
	} else {
		result, err = reviewcontext.AnalyzeWithReview(flags.Arg(0), *reviewPath)
	}
	if err != nil {
		fmt.Fprintf(stderr, "review-context: %v\n", err)
		return 1
	}
	if *format == "json" {
		err = reviewcontext.WriteJSON(stdout, result)
	} else {
		err = reviewcontext.WriteMarkdown(stdout, result)
	}
	if err != nil {
		fmt.Fprintf(stderr, "review-context: write output: %v\n", err)
		return 1
	}
	return 0
}
