package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"stock-watcher/internal/config"
	"stock-watcher/internal/market"
	"strings"
	"time"
)

type result struct {
	Name    string         `json:"input_name"`
	Query   string         `json:"query"`
	Matches []config.Stock `json:"matches"`
	Error   string         `json:"error,omitempty"`
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: resolve-pool names.txt results.json")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot read names")
		os.Exit(1)
	}
	c := market.NewClient(10 * time.Second)
	defer c.Close()
	out := []result{}
	aliases := map[string]string{"国际辅材": "国际复材", "山东玻钎": "山东玻纤", "媛姐科技": "源杰科技", "华瑞微": "华润微", "洛阳泪业": "洛阳钼业", "XD老凤祥": "老凤祥"}
	for i, name := range strings.FieldsFunc(string(data), func(r rune) bool { return r == '、' || r == '\n' || r == '，' || r == ',' }) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		query := name
		if v, ok := aliases[name]; ok {
			query = v
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		matches, err := c.Search(ctx, query)
		cancel()
		r := result{Name: name, Query: query, Matches: matches}
		if err != nil {
			r.Error = err.Error()
		}
		out = append(out, r)
		if (i+1)%10 == 0 {
			fmt.Fprintf(os.Stderr, "resolved %d names\n", i+1)
		}
		time.Sleep(350 * time.Millisecond)
	}
	encoded, _ := json.MarshalIndent(struct {
		Version int      `json:"schema_version"`
		Results []result `json:"results"`
	}{1, out}, "", "  ")
	if err := os.WriteFile(os.Args[2], encoded, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "cannot save results")
		os.Exit(1)
	}
}
