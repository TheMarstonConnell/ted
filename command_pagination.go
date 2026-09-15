package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type listPagination struct {
	page     int
	pageSize int
	all      bool
}

func addListPagination(cmd *cobra.Command) *listPagination {
	p := &listPagination{}
	cmd.Flags().IntVar(&p.page, "page", 1, "Result page (1-based)")
	cmd.Flags().IntVar(&p.pageSize, "page-size", 25, "Number of results per page")
	cmd.Flags().BoolVar(&p.all, "all", false, "List all results (cannot be combined with pagination flags)")
	cmd.MarkFlagsMutuallyExclusive("all", "page")
	cmd.MarkFlagsMutuallyExclusive("all", "page-size")
	return p
}

func (p *listPagination) validate() error {
	if p.page < 1 || p.pageSize < 1 {
		return fmt.Errorf("--page and --page-size must be greater than zero")
	}
	if p.page-1 > int(^uint(0)>>1)/p.pageSize {
		return fmt.Errorf("--page and --page-size are too large")
	}
	return nil
}

func (p *listPagination) bounds(total int) (int, int) {
	if p.all {
		return 0, total
	}
	start := min((p.page-1)*p.pageSize, total)
	return start, start + min(p.pageSize, total-start)
}

func (p *listPagination) report(cmd *cobra.Command, args []string, total int, noun string) error {
	if p.all || (p.page == 1 && total <= p.pageSize) {
		return nil
	}
	start, end := p.bounds(total)
	if start == end {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "No results on page %d (%d %s total).\n", p.page, total, noun)
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Showing %d–%d of %d %s.\n", start+1, end, total, noun); err != nil {
		return err
	}
	if end < total {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Next: %s\n", p.nextCommand(cmd, args))
		return err
	}
	return nil
}

var shellWord = regexp.MustCompile(`^[a-zA-Z0-9_@%+=:,./-]+$`)

func quoteShellWord(s string) string {
	if shellWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func (p *listPagination) nextCommand(cmd *cobra.Command, args []string) string {
	parts := strings.Fields(cmd.CommandPath())
	if parts[0] != "ted" {
		parts = append([]string{"ted"}, parts...)
	}
	for _, arg := range args {
		parts = append(parts, quoteShellWord(arg))
	}
	flags := map[string]*pflag.Flag{}
	collect := func(f *pflag.Flag) {
		if f.Name != "page" && f.Name != "all" {
			flags[f.Name] = f
		}
	}
	cmd.Flags().Visit(collect)
	cmd.InheritedFlags().Visit(collect)
	names := make([]string, 0, len(flags))
	for name := range flags {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := flags[name]
		if f.Value.Type() == "bool" {
			parts = append(parts, "--"+name+"="+f.Value.String())
		} else {
			parts = append(parts, "--"+name, quoteShellWord(f.Value.String()))
		}
	}
	parts = append(parts, "--page", strconv.Itoa(p.page+1))
	return strings.Join(parts, " ")
}

func (p *listPagination) browserData(cmd *cobra.Command, key string, data any) (any, error) {
	fields, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("browser response is not an object")
	}
	raw, ok := fields[key]
	if !ok {
		return nil, fmt.Errorf("browser response is missing %q", key)
	}
	items, ok := raw.([]any)
	if raw != nil && !ok {
		return nil, fmt.Errorf("browser %s is not a list", key)
	}
	if items == nil {
		items = []any{}
	}
	start, end := p.bounds(len(items))
	fields[key] = items[start:end]
	metadata := struct {
		Page        int    `json:"page"`
		PageSize    int    `json:"page_size"`
		Total       int    `json:"total"`
		HasMore     bool   `json:"has_more"`
		NextPage    int    `json:"next_page,omitempty"`
		NextCommand string `json:"next_command,omitempty"`
	}{Page: p.page, PageSize: p.pageSize, Total: len(items), HasMore: end < len(items)}
	if metadata.HasMore {
		metadata.NextPage = p.page + 1
		metadata.NextCommand = p.nextCommand(cmd, nil)
	}
	fields["pagination"] = metadata
	return fields, nil
}
